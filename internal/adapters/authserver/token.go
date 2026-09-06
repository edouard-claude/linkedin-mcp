package authserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// PKCE code verifier bounds, RFC 7636 §4.1.
const (
	minVerifierLen = 43
	maxVerifierLen = 128
)

// tokenResponse is the RFC 6749 §5.1 success body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// TokenHandler serves POST /oauth/token for both supported grants.
func (s *Server) TokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeOAuthError(w, http.StatusBadRequest, errInvalidRequest, "corps de requête illisible")
			return
		}
		switch grant := r.PostForm.Get("grant_type"); grant {
		case "authorization_code":
			s.grantAuthorizationCode(w, r)
		case "refresh_token":
			s.grantRefreshToken(w, r)
		default:
			writeOAuthError(w, http.StatusBadRequest, errUnsupportedGrantType,
				"grant_type doit valoir authorization_code ou refresh_token")
		}
	})
}

// grantAuthorizationCode exchanges a single-use code plus its PKCE verifier
// for an access token and a refresh token.
func (s *Server) grantAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	var (
		code        = r.PostForm.Get("code")
		verifier    = r.PostForm.Get("code_verifier")
		redirectURI = r.PostForm.Get("redirect_uri")
		clientID    = r.PostForm.Get("client_id")
	)
	if code == "" || verifier == "" || clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest,
			"code, code_verifier et client_id sont obligatoires")
		return
	}
	if len(verifier) < minVerifierLen || len(verifier) > maxVerifierLen {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest,
			"code_verifier doit faire entre 43 et 128 caractères")
		return
	}

	// Consuming the code deletes it, so a replay of the same code lands in
	// the ErrNotFound branch below whatever else the attacker gets right.
	authCode, err := s.store.ConsumeAuthCode(r.Context(), code)
	if errors.Is(err, domain.ErrNotFound) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "code inconnu, déjà utilisé ou expiré")
		return
	}
	if err != nil {
		s.logger.Error("consommation du code", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
		return
	}
	if !s.clock.Now().Before(authCode.ExpiresAt) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "code expiré")
		return
	}
	if subtle.ConstantTimeCompare([]byte(authCode.ClientID), []byte(clientID)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "code émis pour un autre client")
		return
	}
	// redirect_uri is optional in the request only when it was absent from
	// the authorization request, which cannot happen here: we always store it.
	if redirectURI != authCode.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant,
			"redirect_uri différente de celle de la demande d'autorisation")
		return
	}
	if !verifyPKCE(verifier, authCode.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "code_verifier invalide")
		return
	}
	if resource := r.PostForm.Get("resource"); resource != "" && resource != s.opts.Resource {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest,
			"resource ne correspond pas à ce serveur MCP")
		return
	}

	s.issueTokens(w, r, authCode.TenantID, authCode.ClientID)
}

// grantRefreshToken rotates a refresh token: the presented one is revoked and
// a brand new pair is issued.
func (s *Server) grantRefreshToken(w http.ResponseWriter, r *http.Request) {
	presented := r.PostForm.Get("refresh_token")
	clientID := r.PostForm.Get("client_id")
	if presented == "" || clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest,
			"refresh_token et client_id sont obligatoires")
		return
	}

	stored, err := s.store.RotateRefreshToken(r.Context(), hashToken(presented), s.clock.Now())
	if errors.Is(err, domain.ErrNotFound) {
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant,
			"refresh_token inconnu, déjà utilisé, révoqué ou expiré")
		return
	}
	if err != nil {
		s.logger.Error("rotation du refresh token", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
		return
	}
	if subtle.ConstantTimeCompare([]byte(stored.ClientID), []byte(clientID)) != 1 {
		writeOAuthError(w, http.StatusBadRequest, errUnauthorizedClient,
			"refresh_token émis pour un autre client")
		return
	}

	s.issueTokens(w, r, stored.TenantID, stored.ClientID)
}

// issueTokens mints the access token and the next refresh token, and writes
// the response.
func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, tenantID, clientID string) {
	accessToken, expiry, err := s.MintAccessToken(tenantID, clientID)
	if err != nil {
		s.logger.Error("émission du jeton d'accès", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
		return
	}
	refreshToken, err := randomSecret()
	if err != nil {
		s.logger.Error("génération du refresh token", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
		return
	}
	record := &domain.RefreshToken{
		TokenHash: hashToken(refreshToken),
		ClientID:  clientID,
		TenantID:  tenantID,
		ExpiresAt: s.clock.Now().Add(s.opts.RefreshTokenTTL),
	}
	if err := s.store.CreateRefreshToken(r.Context(), record); err != nil {
		s.logger.Error("enregistrement du refresh token", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(expiry.Sub(s.clock.Now()).Seconds()),
		RefreshToken: refreshToken,
	})
}

// verifyPKCE checks a code verifier against the stored S256 challenge.
func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
