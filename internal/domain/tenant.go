// Package domain holds the entities and the ports of the application. It
// depends on nothing but the standard library: no HTTP, no SQL, no LinkedIn.
package domain

import (
	"strings"
	"time"
)

// Tenant is one LinkedIn member using the server. Every row in the system
// belongs to exactly one, and is never visible to another.
type Tenant struct {
	ID          string `json:"id"`
	MemberID    string `json:"member_id"`
	DisplayName string `json:"display_name"`
	Headline    string `json:"headline,omitempty"`
	AccessToken string `json:"-"` // never serialized
	// RefreshToken is usually empty: LinkedIn only issues programmatic
	// refresh tokens to approved partners.
	RefreshToken   string    `json:"-"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitzero"`
	Scopes         []string  `json:"scopes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MemberURN is the author URN every write needs.
func (t *Tenant) MemberURN() string { return "urn:li:person:" + t.MemberID }

// TokenExpiresWithin reports whether the access token is due for renewal. A
// zero deadline means unknown, which counts as due.
func (t *Tenant) TokenExpiresWithin(now time.Time, window time.Duration) bool {
	if t.TokenExpiresAt.IsZero() {
		return true
	}
	return t.TokenExpiresAt.Before(now.Add(window))
}

// HasScope reports whether the member granted a permission.
func (t *Tenant) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Member is the identity behind an access token, from the OpenID userinfo
// endpoint.
type Member struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	Picture string `json:"picture,omitempty"`
	Locale  string `json:"locale,omitempty"`
}

// Token is what LinkedIn returns from the token endpoint.
type Token struct {
	AccessToken  string
	ExpiresIn    time.Duration
	RefreshToken string
	// RefreshExpiresIn is zero when no refresh token was issued.
	RefreshExpiresIn time.Duration
	Scopes           []string
}

// ParseScopes splits the space delimited scope string LinkedIn returns.
func ParseScopes(raw string) []string {
	out := []string{}
	for _, s := range strings.Fields(raw) {
		out = append(out, s)
	}
	return out
}

// OAuthClient is an MCP client registered through dynamic client registration.
type OAuthClient struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	CreatedAt    time.Time
}

// AllowsRedirectURI reports whether uri is one of the exact registered URIs.
func (c *OAuthClient) AllowsRedirectURI(uri string) bool {
	for _, u := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

// AuthCode is a single-use OAuth 2.1 authorization code bound to a PKCE
// challenge and to the tenant that authenticated at LinkedIn.
type AuthCode struct {
	Code          string
	ClientID      string
	TenantID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	ExpiresAt     time.Time
}

// RefreshToken is stored hashed; the plaintext only exists in the response.
type RefreshToken struct {
	TokenHash string
	ClientID  string
	TenantID  string
	ExpiresAt time.Time
	Revoked   bool
}

// LoginState carries the pending MCP authorization request across the
// LinkedIn login round trip, and doubles as the CSRF token.
type LoginState struct {
	State     string
	Request   OAuthRequest
	ExpiresAt time.Time
}

// OAuthRequest is the MCP client's /oauth/authorize request, parked while the
// member authenticates against LinkedIn.
type OAuthRequest struct {
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	ClientState   string `json:"client_state"`
	Resource      string `json:"resource"`
}
