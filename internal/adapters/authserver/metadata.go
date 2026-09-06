package authserver

import "net/http"

// protectedResourceMetadata is the RFC 9728 document that tells an MCP client
// which authorization server guards /mcp.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// authServerMetadata is the RFC 8414 document. jwks_uri is deliberately
// absent: access tokens are signed with a symmetric key that only this server
// holds, so there is no public key to publish.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// ProtectedResourceMetadataHandler serves
// GET /.well-known/oauth-protected-resource.
func (s *Server) ProtectedResourceMetadataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, protectedResourceMetadata{
			Resource:               s.opts.Resource,
			AuthorizationServers:   []string{s.opts.Issuer},
			BearerMethodsSupported: []string{"header"},
		})
	})
}

// AuthServerMetadataHandler serves
// GET /.well-known/oauth-authorization-server.
func (s *Server) AuthServerMetadataHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, authServerMetadata{
			Issuer:                            s.opts.Issuer,
			AuthorizationEndpoint:             s.opts.Issuer + "/oauth/authorize",
			TokenEndpoint:                     s.opts.Issuer + "/oauth/token",
			RegistrationEndpoint:              s.opts.Issuer + "/oauth/register",
			ResponseTypesSupported:            []string{"code"},
			GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
			CodeChallengeMethodsSupported:     []string{"S256"},
			TokenEndpointAuthMethodsSupported: []string{"none"},
		})
	})
}
