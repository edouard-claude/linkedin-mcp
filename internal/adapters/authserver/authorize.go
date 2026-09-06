package authserver

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// AuthorizeHandler serves GET /oauth/authorize.
//
// It validates the client and the PKCE parameters, parks the request under a
// random state, and sends the browser to the Facebook login page. The MCP
// client is redirected back only once Meta has confirmed who the user is.
func (s *Server) AuthorizeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")

		// Until the client and its redirect_uri are proven, an error must be
		// rendered here and never redirected: redirecting to an unverified
		// URI would turn this endpoint into an open redirector.
		if clientID == "" {
			s.authorizeError(w, http.StatusBadRequest, errInvalidRequest, "client_id manquant")
			return
		}
		client, err := s.store.ClientByID(r.Context(), clientID)
		if errors.Is(err, domain.ErrNotFound) {
			s.authorizeError(w, http.StatusUnauthorized, errInvalidClient, "client_id inconnu")
			return
		}
		if err != nil {
			s.logger.Error("lecture du client OAuth", "error", err)
			s.authorizeError(w, http.StatusInternalServerError, errServerError, "erreur interne")
			return
		}
		if redirectURI == "" || !client.AllowsRedirectURI(redirectURI) {
			s.authorizeError(w, http.StatusBadRequest, errInvalidRequest,
				"redirect_uri absente ou non enregistrée pour ce client")
			return
		}

		// From here the redirect_uri is trusted, so protocol errors go back
		// to the client as the specification requires.
		clientState := q.Get("state")
		if rt := q.Get("response_type"); rt != "code" {
			s.redirectError(w, r, redirectURI, clientState, errInvalidRequest,
				"seul response_type=code est supporté")
			return
		}
		challenge := q.Get("code_challenge")
		if challenge == "" {
			s.redirectError(w, r, redirectURI, clientState, errInvalidRequest,
				"code_challenge est obligatoire (PKCE)")
			return
		}
		if method := q.Get("code_challenge_method"); method != "S256" {
			s.redirectError(w, r, redirectURI, clientState, errInvalidRequest,
				"seul code_challenge_method=S256 est supporté")
			return
		}
		resource := q.Get("resource")
		if resource != "" && resource != s.opts.Resource {
			s.redirectError(w, r, redirectURI, clientState, errInvalidRequest,
				"resource ne correspond pas à ce serveur MCP")
			return
		}

		state, err := randomSecret()
		if err != nil {
			s.logger.Error("génération du state", "error", err)
			s.redirectError(w, r, redirectURI, clientState, errServerError, "erreur interne")
			return
		}
		login := &domain.LoginState{
			State: state,
			Request: domain.OAuthRequest{
				ClientID:      clientID,
				RedirectURI:   redirectURI,
				CodeChallenge: challenge,
				ClientState:   clientState,
				Resource:      resource,
			},
			ExpiresAt: s.clock.Now().Add(LoginStateTTL),
		}
		if err := s.store.CreateLoginState(r.Context(), login); err != nil {
			s.logger.Error("enregistrement du state", "error", err)
			s.redirectError(w, r, redirectURI, clientState, errServerError, "erreur interne")
			return
		}

		target := s.opts.LoginPath + "?state=" + url.QueryEscape(state)
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// authorizeError renders an error that cannot be delivered to the client.
func (s *Server) authorizeError(w http.ResponseWriter, status int, code, description string) {
	writeOAuthError(w, status, code, description)
}

// redirectError hands a protocol error back to the MCP client through its
// registered redirect_uri, per RFC 6749 §4.1.2.1.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	params := map[string]string{"error": code, "error_description": description}
	if state != "" {
		params["state"] = state
	}
	http.Redirect(w, r, redirectWithParams(redirectURI, params), http.StatusFound)
}

// redirectWithParams appends query parameters to a redirect URI, preserving
// any the client already put there.
func redirectWithParams(redirectURI string, params map[string]string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
