package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// DefaultRefreshWindow is how long before expiry a token is renewed. LinkedIn
// issues 60 day access tokens, so two weeks of margin leaves room for a
// server that was down without letting anyone drop off.
const DefaultRefreshWindow = 14 * 24 * time.Hour

// recheckInterval is how long a tenant with no known deadline is left alone
// between checks.
const recheckInterval = 7 * 24 * time.Hour

// LoginService turns a successful LinkedIn login into a tenant.
type LoginService struct {
	store domain.TenantStore
	api   domain.LinkedInOAuth
	clock domain.Clock
	allow func(memberID string) bool
}

// NewLoginService wires the login use case. A nil allow function lets
// everyone in.
func NewLoginService(store domain.TenantStore, api domain.LinkedInOAuth, clk domain.Clock, allow func(string) bool) *LoginService {
	if allow == nil {
		allow = func(string) bool { return true }
	}
	return &LoginService{store: store, api: api, clock: clk, allow: allow}
}

// AuthorizeURL is the LinkedIn consent page the member must visit.
func (s *LoginService) AuthorizeURL(redirectURI, state string) string {
	return s.api.AuthorizeURL(redirectURI, state)
}

// LoginResult describes the tenant behind a completed login.
type LoginResult struct {
	TenantID    string
	DisplayName string
	Scopes      []string
}

// Complete runs the callback: it exchanges the code for an access token,
// identifies the member, and creates or refreshes their tenant.
func (s *LoginService) Complete(ctx context.Context, code, redirectURI string) (*LoginResult, error) {
	token, err := s.api.ExchangeCode(ctx, code, redirectURI)
	if err != nil {
		return nil, err
	}
	member, err := s.api.Me(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}
	if !s.allow(member.ID) {
		return nil, domain.ErrForbiddenMember
	}

	now := s.clock.Now()
	tenant := &domain.Tenant{
		ID:             newTenantID(),
		MemberID:       member.ID,
		DisplayName:    member.Name,
		AccessToken:    token.AccessToken,
		RefreshToken:   token.RefreshToken,
		TokenExpiresAt: expiryFrom(now, token.ExpiresIn),
		Scopes:         token.Scopes,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	// A returning member keeps the tenant id they already had, so their MCP
	// clients stay authorized against the same subject.
	existing, err := s.store.TenantByMemberID(ctx, member.ID)
	switch {
	case err == nil:
		tenant.ID = existing.ID
		tenant.CreatedAt = existing.CreatedAt
	case errors.Is(err, domain.ErrNotFound):
	default:
		return nil, fmt.Errorf("recherche du tenant: %w", err)
	}

	if err := s.store.UpsertTenant(ctx, tenant); err != nil {
		return nil, fmt.Errorf("enregistrement du tenant: %w", err)
	}
	return &LoginResult{TenantID: tenant.ID, DisplayName: tenant.DisplayName, Scopes: tenant.Scopes}, nil
}

// expiryFrom turns a relative lifetime into an absolute deadline. Zero means
// unknown, which the refresh sweep reads as "check at the next opportunity".
func expiryFrom(now time.Time, lifetime time.Duration) time.Time {
	if lifetime <= 0 {
		return time.Time{}
	}
	return now.Add(lifetime)
}

// ConsumeState retrieves and burns the login state parked by the
// authorization endpoint, checking that it has not expired.
func (s *LoginService) ConsumeState(ctx context.Context, state string) (*domain.LoginState, error) {
	login, err := s.store.ConsumeLoginState(ctx, state)
	if err != nil {
		return nil, err
	}
	if !s.clock.Now().Before(login.ExpiresAt) {
		return nil, domain.ErrNotFound
	}
	return login, nil
}

// DeleteByMemberID removes a tenant and everything attached to it.
func (s *LoginService) DeleteByMemberID(ctx context.Context, memberID string) error {
	tenant, err := s.store.TenantByMemberID(ctx, memberID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recherche du tenant à supprimer: %w", err)
	}
	if err := s.store.DeleteTenant(ctx, tenant.ID); err != nil {
		return fmt.Errorf("suppression du tenant: %w", err)
	}
	return nil
}

// RefreshReport summarizes one renewal sweep.
type RefreshReport struct {
	Checked   int
	Refreshed int
	// NeedsReconnect lists tenants whose token cannot be renewed without the
	// member going through the browser again. On LinkedIn that is the normal
	// case: programmatic refresh tokens go to approved partners only.
	NeedsReconnect []string
}

// RefreshExpiringTokens renews what it can and reports what it cannot.
//
// Unlike Meta, LinkedIn does not hand every app a refresh token, so this
// sweep is mostly a detector: it tells the operator which members must
// reconnect before their 60 days run out, rather than silently fixing it.
func (s *LoginService) RefreshExpiringTokens(ctx context.Context, window time.Duration) (RefreshReport, error) {
	if window <= 0 {
		window = DefaultRefreshWindow
	}
	now := s.clock.Now()

	tenants, err := s.store.TenantsDueForTokenRefresh(ctx, now.Add(window), now.Add(-recheckInterval))
	if err != nil {
		return RefreshReport{}, fmt.Errorf("liste des jetons à renouveler: %w", err)
	}

	report := RefreshReport{Checked: len(tenants)}
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := s.refreshOne(ctx, tenant); err != nil {
			report.NeedsReconnect = append(report.NeedsReconnect, tenant.ID)
			continue
		}
		report.Refreshed++
	}
	return report, nil
}

func (s *LoginService) refreshOne(ctx context.Context, tenant domain.Tenant) error {
	token, err := s.api.RefreshAccessToken(ctx, tenant.RefreshToken)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	updated := tenant
	updated.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		updated.RefreshToken = token.RefreshToken
	}
	updated.TokenExpiresAt = expiryFrom(now, token.ExpiresIn)
	updated.UpdatedAt = now
	if len(token.Scopes) > 0 {
		updated.Scopes = token.Scopes
	}

	if err := s.store.UpsertTenant(ctx, &updated); err != nil {
		return fmt.Errorf("enregistrement du jeton renouvelé: %w", err)
	}
	return nil
}
