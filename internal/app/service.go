// Package app holds the use cases: the login flow and the MCP tools. It
// depends on the domain ports only, never on HTTP, SQL or LinkedIn.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// LoginStateTTL bounds how long a parked login request stays valid.
const LoginStateTTL = 10 * time.Minute

// previewNotice is what a write preview tells the caller to do next.
const previewNotice = "Aperçu uniquement, rien n'a été envoyé à LinkedIn. " +
	"Montrez ce contenu à l'utilisateur et attendez son accord explicite, " +
	"puis rappelez le même outil avec confirm=true."

// Service implements the MCP tools. Every method takes the tenant id resolved
// from the bearer token, and none can reach another tenant's data.
type Service struct {
	store domain.TenantStore
	api   domain.Client
	clock domain.Clock
	// publicURL is this server's base URL, used to build reconnection links.
	publicURL string
	// requestedScopes is what the login dialog asks for, used by
	// connection_status to spot a permission the member did not grant.
	requestedScopes []string
}

// NewService wires the tool use cases.
func NewService(store domain.TenantStore, api domain.Client, clk domain.Clock, publicURL string, scopes []string) *Service {
	return &Service{store: store, api: api, clock: clk, publicURL: publicURL, requestedScopes: scopes}
}

// tenant loads the tenant behind a request.
func (s *Service) tenant(ctx context.Context, tenantID string) (*domain.Tenant, error) {
	t, err := s.store.TenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// requireScope refuses early when the member never granted a permission,
// which produces a clearer message than LinkedIn's bare 403.
func requireScope(t *domain.Tenant, scope, why string) error {
	if t.HasScope(scope) {
		return nil
	}
	return &domain.ErrScopeMissing{Scope: scope, Why: why}
}

// ownedPost checks that a post URN belongs to this tenant's ledger.
//
// It is what keeps one member from acting on another's post through a URN
// they happen to know. A post published outside this server is unknown to the
// ledger, so writes on it are refused unless the caller passes allowUnknown.
func (s *Service) ownedPost(ctx context.Context, tenantID, postURN string) (*domain.LedgerPost, error) {
	if strings.TrimSpace(postURN) == "" {
		return nil, errors.New("post_urn est obligatoire")
	}
	if !strings.HasPrefix(postURN, "urn:li:") {
		return nil, errors.New("post_urn doit être une URN LinkedIn, par exemple urn:li:share:1234")
	}
	p, err := s.store.LedgerPost(ctx, tenantID, postURN)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("publication inconnue de ce compte : elle n'a pas été créée depuis ce serveur, "+
			"ou elle appartient à quelqu'un d'autre (%s)", postURN)
	}
	if err != nil {
		return nil, fmt.Errorf("lecture du registre: %w", err)
	}
	return p, nil
}

// newTenantID returns a random UUID v4.
func newTenantID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// newSecret returns 32 bytes of randomness, base64url encoded.
func newSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// clampLimit keeps a caller supplied limit inside the accepted range.
func clampLimit(limit, def, max int) int {
	switch {
	case limit <= 0:
		return def
	case limit > max:
		return max
	default:
		return limit
	}
}

// dayLayout is the date format the analytics tools accept.
const dayLayout = "2006-01-02"

// optionalDay parses a bound that may be absent.
func optionalDay(value, name string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	day, err := time.ParseInLocation(dayLayout, value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s doit être au format AAAA-MM-JJ", name)
	}
	return day, nil
}

// metricsOrDefault falls back to the default metric set.
func metricsOrDefault(requested, fallback []string) []string {
	cleaned := make([]string, 0, len(requested))
	for _, m := range requested {
		if m = strings.TrimSpace(m); m != "" {
			cleaned = append(cleaned, strings.ToUpper(m))
		}
	}
	if len(cleaned) == 0 {
		return fallback
	}
	return cleaned
}

// ReconnectURL builds a single use link the member opens to renew their
// LinkedIn authorization.
func (s *Service) ReconnectURL(ctx context.Context) (string, error) {
	state, err := newSecret()
	if err != nil {
		return "", fmt.Errorf("génération du state: %w", err)
	}
	login := &domain.LoginState{
		State:     state,
		Request:   domain.OAuthRequest{},
		ExpiresAt: s.clock.Now().Add(LoginStateTTL),
	}
	if err := s.store.CreateLoginState(ctx, login); err != nil {
		return "", fmt.Errorf("enregistrement du state: %w", err)
	}
	return s.publicURL + "/linkedin/login?state=" + state, nil
}
