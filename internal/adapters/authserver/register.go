package authserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// maxRedirectURIs bounds what a single client may register.
const maxRedirectURIs = 10

// registrationRequest is the subset of RFC 7591 metadata this server reads.
// Anything else a client sends is accepted and ignored.
type registrationRequest struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// registrationResponse echoes the registered metadata. There is no client
// secret: MCP clients are public clients and authenticate with PKCE alone.
type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// RegisterHandler serves POST /oauth/register (RFC 7591).
func (s *Server) RegisterHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var req registrationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOAuthError(w, http.StatusBadRequest, errInvalidClientMeta,
				"corps JSON illisible")
			return
		}
		if len(req.RedirectURIs) == 0 {
			writeOAuthError(w, http.StatusBadRequest, errInvalidClientMeta,
				"redirect_uris est obligatoire")
			return
		}
		if len(req.RedirectURIs) > maxRedirectURIs {
			writeOAuthError(w, http.StatusBadRequest, errInvalidClientMeta,
				"trop de redirect_uris")
			return
		}
		for _, uri := range req.RedirectURIs {
			if err := validateRedirectURI(uri); err != nil {
				writeOAuthError(w, http.StatusBadRequest, errInvalidClientMeta, err.Error())
				return
			}
		}

		clientID, err := randomSecret()
		if err != nil {
			s.logger.Error("génération client_id", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
			return
		}
		client := &domain.OAuthClient{
			ClientID:     clientID,
			ClientName:   strings.TrimSpace(req.ClientName),
			RedirectURIs: req.RedirectURIs,
			CreatedAt:    s.clock.Now(),
		}
		if err := s.store.RegisterClient(r.Context(), client); err != nil {
			s.logger.Error("enregistrement du client", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, errServerError, "erreur interne")
			return
		}
		s.logger.Info("client MCP enregistré", "client_name", client.ClientName)

		writeJSON(w, http.StatusCreated, registrationResponse{
			ClientID:                client.ClientID,
			ClientIDIssuedAt:        client.CreatedAt.Unix(),
			ClientName:              client.ClientName,
			RedirectURIs:            client.RedirectURIs,
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		})
	})
}

// validateRedirectURI enforces the OAuth 2.1 rule: HTTPS everywhere, with the
// documented exception of the loopback interface, which desktop MCP clients
// need to catch the redirect on an ephemeral port.
func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errInvalidURI("redirect_uri illisible")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return errInvalidURI("redirect_uri ne doit pas contenir de fragment")
	}
	switch u.Scheme {
	case "https":
		if u.Host == "" {
			return errInvalidURI("redirect_uri sans hôte")
		}
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return errInvalidURI("redirect_uri en http n'est autorisée que sur localhost")
	default:
		return errInvalidURI("redirect_uri doit être en https, ou en http sur localhost")
	}
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// errInvalidURI keeps the validation messages short and in French; they are
// returned verbatim in the OAuth error description.
type errInvalidURI string

func (e errInvalidURI) Error() string { return string(e) }
