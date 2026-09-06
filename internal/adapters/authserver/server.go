// Package authserver is the OAuth 2.1 authorization server that MCP clients
// talk to. It implements RFC 6749 (authorization code), RFC 7636 (PKCE S256,
// mandatory), RFC 7591 (dynamic client registration), RFC 8414 (server
// metadata), RFC 9728 (protected resource metadata) and RFC 8707 (resource
// indicators), as required by the MCP authorization specification.
//
// End user authentication itself is federated to Facebook: this package parks
// the client's authorization request, hands over to the Meta login flow, and
// is called back with the tenant that authenticated.
package authserver

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// TTLs of the short-lived artefacts of the flow.
const (
	// AuthCodeTTL is the lifetime of an authorization code (OAuth 2.1
	// recommends one minute or less; five is the SPEC value and stays well
	// within what clients need for a redirect round trip).
	AuthCodeTTL = 5 * time.Minute
	// LoginStateTTL bounds how long a user has to complete the Facebook
	// login before the parked request is dropped.
	LoginStateTTL = 10 * time.Minute
	// secretBytes is the entropy of every code, state and refresh token.
	secretBytes = 32
)

// Options configures the authorization server. Everything is derived from
// PUBLIC_URL by the composition root.
type Options struct {
	// Issuer is the public base URL of this server, used as the JWT `iss`.
	Issuer string
	// Resource is the canonical MCP endpoint URL, used as the JWT `aud` and
	// as the RFC 8707 resource indicator.
	Resource string
	// SigningKey is the HMAC-SHA256 key of the access tokens.
	SigningKey []byte
	// LoginPath is where the user is sent to authenticate against Facebook.
	LoginPath string

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// Server holds the handlers of the OAuth surface.
type Server struct {
	store  domain.TenantStore
	clock  domain.Clock
	opts   Options
	logger *slog.Logger
}

// New builds the authorization server.
func New(store domain.TenantStore, clock domain.Clock, opts Options, logger *slog.Logger) *Server {
	if opts.LoginPath == "" {
		opts.LoginPath = "/meta/login"
	}
	return &Server{store: store, clock: clock, opts: opts, logger: logger}
}

// IssueAuthCode mints an authorization code for a tenant that just proved its
// identity at Meta, and returns the URL the browser must be redirected to.
// It is the bridge between the Meta callback and the waiting MCP client.
func (s *Server) IssueAuthCode(r *http.Request, req domain.OAuthRequest, tenantID string) (string, error) {
	code, err := randomSecret()
	if err != nil {
		return "", err
	}
	authCode := &domain.AuthCode{
		Code:          code,
		ClientID:      req.ClientID,
		TenantID:      tenantID,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		Resource:      req.Resource,
		ExpiresAt:     s.clock.Now().Add(AuthCodeTTL),
	}
	if err := s.store.CreateAuthCode(r.Context(), authCode); err != nil {
		return "", err
	}
	return redirectWithParams(req.RedirectURI, map[string]string{
		"code":  code,
		"state": req.ClientState,
	}), nil
}

// randomSecret returns 32 bytes of cryptographic randomness, base64url
// encoded without padding. Used for codes, states, client ids and refresh
// tokens alike.
func randomSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// oauthError is the error envelope of RFC 6749 §5.2.
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// OAuth error codes used by this server.
const (
	errInvalidRequest       = "invalid_request"
	errInvalidClient        = "invalid_client"
	errInvalidGrant         = "invalid_grant"
	errUnauthorizedClient   = "unauthorized_client"
	errUnsupportedGrantType = "unsupported_grant_type"
	errInvalidClientMeta    = "invalid_client_metadata"
	errServerError          = "server_error"
)

// writeOAuthError answers with the JSON error envelope. Descriptions are in
// French: they surface in the client UI, in front of the end user.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, oauthError{Code: code, Description: description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// allowCORS opens the OAuth endpoints to browser based MCP clients, which
// fetch them cross-origin. These endpoints carry no cookie and no ambient
// authority, so a wildcard origin is safe.
func allowCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Protocol-Version")
}
