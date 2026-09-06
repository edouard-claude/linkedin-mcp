// Package config reads and validates the runtime configuration from the
// environment. The binary refuses to start on an invalid configuration rather
// than failing later on the first request.
package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// Environment variable names, in the order of the SPEC table.
const (
	EnvPublicURL        = "PUBLIC_URL"
	EnvListenAddr       = "LISTEN_ADDR"
	EnvDBPath           = "DB_PATH"
	EnvTokenCipherKey   = "TOKEN_CIPHER_KEY"
	EnvJWTSigningKey    = "JWT_SIGNING_KEY"
	EnvClientID         = "LINKEDIN_CLIENT_ID"
	EnvClientSecret     = "LINKEDIN_CLIENT_SECRET"
	EnvAPIVersion       = "LINKEDIN_API_VERSION"
	EnvScopes           = "LINKEDIN_SCOPES"
	EnvAccessTokenTTL   = "ACCESS_TOKEN_TTL"
	EnvRefreshTokenTTL  = "REFRESH_TOKEN_TTL"
	EnvLogFormat        = "LOG_FORMAT"
	EnvAllowedMemberIDs = "ALLOWED_MEMBER_IDS"

	// EnvGraphBaseURL and EnvDialogBaseURL point the Graph client somewhere
	// else than Meta. They exist for the end to end test, which runs the
	// real binary against a fake Graph, and are not meant for production.
	// EnvRelayPort enables the loopback OAuth relay on /relay/callback.
	EnvRelayPort = "LOOPBACK_RELAY_PORT"

	EnvAPIBaseURL  = "LINKEDIN_API_BASE_URL"
	EnvAuthBaseURL = "LINKEDIN_AUTH_BASE_URL"
)

const (
	defaultListenAddr      = ":8080"
	defaultDBPath          = "/data/linkedin.db"
	defaultAPIVersion      = "202606"
	defaultAccessTokenTTL  = time.Hour
	defaultRefreshTokenTTL = 720 * time.Hour
	defaultLogFormat       = "json"

	// DefaultScopes is what the login dialog asks for.
	//
	// openid, profile and email identify the member. w_member_social covers
	// posts, comments and reactions alike: that is what the consent screen
	// itself says, and it is the only write scope the self-serve products
	// grant. Every other scope below is restricted, and asking for one the
	// app was not granted fails the whole authorization, so they stay out of
	// the default: add them to LINKEDIN_SCOPES the day LinkedIn approves the
	// Community Management access form.
	DefaultScopes = "openid profile email w_member_social"

	// ScopeAnalytics reads member post statistics. Restricted.
	ScopeAnalytics = "r_member_postAnalytics"
	// ScopeReadPosts lists a member's own posts. Restricted.
	ScopeReadPosts = "r_member_social"
	// ScopeReadFeed reads comments and likes. Restricted.
	ScopeReadFeed = "r_member_social_feed"
	// ScopeWritePosts creates, edits and deletes posts, comments and
	// reactions. Granted by the self-serve "Share on LinkedIn" product.
	ScopeWritePosts = "w_member_social"
	// ScopeWriteFeed widens the writes to other members' posts. Restricted,
	// and not required for anything this server does on the member's own
	// content.
	ScopeWriteFeed = "w_member_social_feed"

	// cipherKeyLen is the AES-256 key size.
	cipherKeyLen = 32
	// minSigningKeyLen is the minimum HMAC-SHA256 key size.
	minSigningKeyLen = 32
)

// Config is the validated configuration of the process.
type Config struct {
	PublicURL       string
	ListenAddr      string
	DBPath          string
	TokenCipherKey  []byte
	JWTSigningKey   []byte
	ClientID        string
	ClientSecret    string
	APIVersion      string
	Scopes          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	LogFormat       string

	// GraphBaseURL and DialogBaseURL override the Meta endpoints. Empty in
	// production, where the client uses graph.facebook.com and
	// www.facebook.com.
	APIBaseURL  string
	AuthBaseURL string

	// RelayPort, when non-zero, exposes /relay/callback which forwards an
	// OAuth callback to 127.0.0.1 on that port.
	RelayPort int

	// AllowedMetaUserIDs, when non-empty, is the whitelist of Facebook user
	// ids allowed to create a tenant.
	AllowedMemberIDs map[string]struct{}
}

// MCPResourceURL is the canonical identifier of the protected resource, used
// as the JWT audience and as the RFC 8707 resource indicator.
func (c *Config) MCPResourceURL() string { return c.PublicURL + "/mcp" }

// RedirectURI is the redirect registered in the LinkedIn app.
func (c *Config) RedirectURI() string { return c.PublicURL + "/linkedin/callback" }

// ResourceMetadataURL is advertised in the WWW-Authenticate header of a 401.
func (c *Config) ResourceMetadataURL() string {
	return c.PublicURL + "/.well-known/oauth-protected-resource"
}

// ScopeList splits LINKEDIN_SCOPES into the individual permissions, which
// connection_status compares against what LinkedIn actually granted.
func (c *Config) ScopeList() []string {
	return domain.ParseScopes(c.Scopes)
}

// IsMemberAllowed reports whether a LinkedIn member id may create a tenant.
func (c *Config) IsMemberAllowed(memberID string) bool {
	if len(c.AllowedMemberIDs) == 0 {
		return true
	}
	_, ok := c.AllowedMemberIDs[memberID]
	return ok
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:       env(EnvListenAddr, defaultListenAddr),
		DBPath:           env(EnvDBPath, defaultDBPath),
		ClientID:         strings.TrimSpace(os.Getenv(EnvClientID)),
		ClientSecret:     strings.TrimSpace(os.Getenv(EnvClientSecret)),
		APIVersion:       env(EnvAPIVersion, defaultAPIVersion),
		Scopes:           env(EnvScopes, DefaultScopes),
		LogFormat:        env(EnvLogFormat, defaultLogFormat),
		APIBaseURL:       strings.TrimRight(os.Getenv(EnvAPIBaseURL), "/"),
		AuthBaseURL:      strings.TrimRight(os.Getenv(EnvAuthBaseURL), "/"),
		AllowedMemberIDs: parseCSVSet(os.Getenv(EnvAllowedMemberIDs)),
	}

	publicURL, err := parsePublicURL(os.Getenv(EnvPublicURL))
	if err != nil {
		return nil, err
	}
	cfg.PublicURL = publicURL

	if cfg.ClientID == "" {
		return nil, fmt.Errorf("%s est obligatoire", EnvClientID)
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("%s est obligatoire", EnvClientSecret)
	}

	if cfg.TokenCipherKey, err = decodeKey(EnvTokenCipherKey, cipherKeyLen, true); err != nil {
		return nil, err
	}
	if cfg.JWTSigningKey, err = decodeKey(EnvJWTSigningKey, minSigningKeyLen, false); err != nil {
		return nil, err
	}

	if raw := strings.TrimSpace(os.Getenv(EnvRelayPort)); raw != "" {
		port, convErr := strconv.Atoi(raw)
		if convErr != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%s doit être un port entre 1 et 65535", EnvRelayPort)
		}
		cfg.RelayPort = port
	}

	if cfg.AccessTokenTTL, err = parseDuration(EnvAccessTokenTTL, defaultAccessTokenTTL); err != nil {
		return nil, err
	}
	if cfg.RefreshTokenTTL, err = parseDuration(EnvRefreshTokenTTL, defaultRefreshTokenTTL); err != nil {
		return nil, err
	}

	switch cfg.LogFormat {
	case "json", "text":
	default:
		return nil, fmt.Errorf("%s doit valoir json ou text, pas %q", EnvLogFormat, cfg.LogFormat)
	}

	if len(cfg.APIVersion) != 6 {
		return nil, fmt.Errorf("%s doit être au format AAAAMM, pas %q", EnvAPIVersion, cfg.APIVersion)
	}
	for _, r := range cfg.APIVersion {
		if r < '0' || r > '9' {
			return nil, fmt.Errorf("%s doit être au format AAAAMM, pas %q", EnvAPIVersion, cfg.APIVersion)
		}
	}

	return cfg, nil
}

// parsePublicURL enforces an absolute https URL without a trailing slash: the
// whole OAuth surface derives its URLs from it, and OAuth 2.1 forbids plain
// HTTP for a public issuer.
func parsePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s est obligatoire", EnvPublicURL)
	}
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s invalide: %w", EnvPublicURL, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("%s doit être en https://, pas %q", EnvPublicURL, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s doit contenir un hôte", EnvPublicURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s ne doit contenir ni query ni fragment", EnvPublicURL)
	}
	return raw, nil
}

// decodeKey decodes a base64 key and checks its size. When exact is true the
// length must match exactly, otherwise it is a minimum.
func decodeKey(name string, size int, exact bool) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, fmt.Errorf("%s est obligatoire", name)
	}
	key, err := decodeBase64(raw)
	if err != nil {
		return nil, fmt.Errorf("%s doit être encodé en base64: %w", name, err)
	}
	if exact && len(key) != size {
		return nil, fmt.Errorf("%s doit faire exactement %d octets, pas %d", name, size, len(key))
	}
	if !exact && len(key) < size {
		return nil, fmt.Errorf("%s doit faire au moins %d octets, pas %d", name, size, len(key))
	}
	return key, nil
}

// decodeBase64 accepts both the standard and the URL-safe alphabet, padded or
// not, because key material gets copied around by hand.
func decodeBase64(raw string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	var err error
	for _, enc := range encodings {
		var out []byte
		if out, err = enc.DecodeString(raw); err == nil {
			return out, nil
		}
	}
	return nil, err
}

func parseDuration(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s invalide: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s doit être strictement positif", name)
	}
	return d, nil
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func parseCSVSet(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out[p] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
