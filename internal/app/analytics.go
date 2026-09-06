package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/config"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// KnownMetrics is what the analytics API accepts, per LinkedIn's own
// documentation of memberCreatorPostAnalytics.
var KnownMetrics = []string{
	"IMPRESSION",
	"MEMBERS_REACHED",
	"RESHARE",
	"REACTION",
	"COMMENT",
	"POST_SAVE",
	"POST_SEND",
	"LINK_CLICKS",
	"PREMIUM_CTA_CLICKS",
	"FOLLOWER_GAINED_FROM_CONTENT",
	"PROFILE_VIEW_FROM_CONTENT",
}

// DefaultMetrics is what the analytics tools read when asked for nothing in
// particular: the five that answer "how did this land".
var DefaultMetrics = []string{
	"IMPRESSION",
	"MEMBERS_REACHED",
	"REACTION",
	"COMMENT",
	"RESHARE",
}

// totalOnlyMetrics cannot be broken down by day. Asking for DAILY on these
// fails the request, so the daily mode drops them instead.
var totalOnlyMetrics = []string{
	"MEMBERS_REACHED",
	"LINK_CLICKS",
	"FOLLOWER_GAINED_FROM_CONTENT",
	"PROFILE_VIEW_FROM_CONTENT",
}

// AnalyticsInput are the parameters of post_analytics and account_analytics.
type AnalyticsInput struct {
	// PostURN empty means the aggregate across every post of the member.
	PostURN string
	Metrics []string
	Daily   bool
	Since   string
	Until   string
}

// AnalyticsOutput carries the metrics and what could not be read.
type AnalyticsOutput struct {
	PostURN     string           `json:"post_urn,omitempty"`
	Permalink   string           `json:"permalink,omitempty"`
	Aggregation string           `json:"aggregation"`
	Insights    []domain.Insight `json:"insights"`
	Rejected    []string         `json:"rejected,omitempty"`
	Notice      string           `json:"notice,omitempty"`
}

// Analytics reads member post statistics, for one post or for the account.
func (s *Service) Analytics(ctx context.Context, tenantID string, in AnalyticsInput) (*AnalyticsOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeAnalytics,
		"elle vient du formulaire d'accès Community Management de LinkedIn, "+
			"et il faut vous reconnecter une fois qu'elle est accordée"); err != nil {
		return nil, err
	}

	metrics := metricsOrDefault(in.Metrics, DefaultMetrics)
	for _, m := range metrics {
		if !slices.Contains(KnownMetrics, m) {
			return nil, fmt.Errorf("métrique inconnue %q, valeurs acceptées : %s",
				m, strings.Join(KnownMetrics, ", "))
		}
	}

	aggregation := domain.AggregationTotal
	var notice string
	if in.Daily {
		aggregation = domain.AggregationDaily
		kept := make([]string, 0, len(metrics))
		var dropped []string
		for _, m := range metrics {
			if slices.Contains(totalOnlyMetrics, m) {
				dropped = append(dropped, m)
				continue
			}
			kept = append(kept, m)
		}
		if len(dropped) > 0 {
			notice = fmt.Sprintf("LinkedIn ne ventile pas ces métriques par jour, elles ont été retirées : %s. "+
				"Redemandez-les sans daily pour obtenir leur total.", strings.Join(dropped, ", "))
		}
		if len(kept) == 0 {
			return nil, errors.New("aucune des métriques demandées n'existe en ventilation quotidienne")
		}
		metrics = kept
	}

	since, err := optionalDay(in.Since, "since")
	if err != nil {
		return nil, err
	}
	until, err := optionalDay(in.Until, "until")
	if err != nil {
		return nil, err
	}
	if !since.IsZero() && !until.IsZero() && !since.Before(until) {
		return nil, errors.New("since doit être antérieur à until")
	}
	// The API needs a start whenever an end is given.
	if since.IsZero() && !until.IsZero() {
		since = until.AddDate(0, 0, -28)
	}

	set, err := s.api.PostAnalytics(ctx, tenant.AccessToken, domain.AnalyticsQuery{
		PostURN:     strings.TrimSpace(in.PostURN),
		Metrics:     metrics,
		Aggregation: aggregation,
		Since:       since,
		Until:       until,
	})
	if err != nil {
		return nil, err
	}

	out := &AnalyticsOutput{
		PostURN:     strings.TrimSpace(in.PostURN),
		Aggregation: aggregation,
		Insights:    set.Insights,
		Rejected:    set.Rejected,
		Notice:      notice,
	}
	if out.PostURN != "" {
		out.Permalink = permalink(out.PostURN)
	}
	return out, nil
}

// ConnectionStatus is what connection_status reports.
type ConnectionStatus struct {
	DisplayName string `json:"display_name"`
	MemberURN   string `json:"member_urn"`
	Healthy     bool   `json:"healthy"`
	Summary     string `json:"summary"`

	TokenActive    bool   `json:"token_active"`
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
	DaysRemaining  int    `json:"days_remaining,omitempty"`
	TokenReason    string `json:"token_reason,omitempty"`

	GrantedScopes []string `json:"granted_scopes,omitempty"`
	MissingScopes []string `json:"missing_scopes,omitempty"`
	// Capabilities says in plain terms what the member can and cannot do.
	Capabilities    map[string]bool `json:"capabilities"`
	PostsRecorded   int             `json:"posts_recorded"`
	ReconnectionURL string          `json:"reconnect_url,omitempty"`
}

// ConnectionStatus asks LinkedIn whether the authorization still holds, and
// translates the permission set into what actually works.
func (s *Service) ConnectionStatus(ctx context.Context, tenantID string) (*ConnectionStatus, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	ledger, err := s.store.LedgerPosts(ctx, tenantID, true, maxPostLimit)
	if err != nil {
		return nil, fmt.Errorf("lecture du registre: %w", err)
	}

	status := &ConnectionStatus{
		DisplayName:   tenant.DisplayName,
		MemberURN:     tenant.MemberURN(),
		GrantedScopes: tenant.Scopes,
		PostsRecorded: len(ledger),
	}
	expiry := tenant.TokenExpiresAt
	introspection, err := s.api.IntrospectToken(ctx, tenant.AccessToken)
	if err != nil {
		status.TokenActive = false
		status.TokenReason = "impossible de joindre LinkedIn pour vérifier le jeton"
	} else {
		status.TokenActive = introspection.Active
		status.TokenReason = introspection.Reason
		if !introspection.ExpiresAt.IsZero() {
			expiry = introspection.ExpiresAt
		}
		if len(introspection.Scopes) > 0 {
			status.GrantedScopes = introspection.Scopes
		}
	}

	// The missing scopes are computed last, against whichever list is
	// authoritative: LinkedIn's answer when we could reach it, the stored one
	// otherwise. Computing them earlier would compare against a stale list
	// and invent permissions that are in fact granted.
	granted := status.GrantedScopes
	// Capabilities are read from the same authoritative list, so the plain
	// language answer and the scope list can never contradict each other.
	status.Capabilities = map[string]bool{
		"publier":               slices.Contains(granted, config.ScopeWritePosts),
		"commenter_et_reagir":   slices.Contains(granted, config.ScopeWriteFeed),
		"lire_ses_publications": slices.Contains(granted, config.ScopeReadPosts),
		"lire_engagement":       slices.Contains(granted, config.ScopeReadPosts),
		"statistiques":          slices.Contains(granted, config.ScopeAnalytics),
	}
	for _, scope := range s.requestedScopes {
		if len(granted) > 0 && !slices.Contains(granted, scope) {
			status.MissingScopes = append(status.MissingScopes, scope)
		}
	}

	now := s.clock.Now()
	if !expiry.IsZero() {
		status.TokenExpiresAt = expiry.UTC().Format(time.RFC3339)
		status.DaysRemaining = int(expiry.Sub(now).Hours() / 24)
	}
	status.Healthy = status.TokenActive && len(status.MissingScopes) == 0

	switch {
	case !status.TokenActive:
		status.Summary = "L'autorisation LinkedIn n'est plus valide, une reconnexion est nécessaire."
	case len(status.MissingScopes) > 0:
		status.Summary = fmt.Sprintf("Connexion active mais %d permission(s) manquante(s) : certaines fonctions échoueront.",
			len(status.MissingScopes))
	case status.TokenExpiresAt == "":
		status.Summary = "Connexion valide, sans date d'expiration connue."
	default:
		status.Summary = fmt.Sprintf("Connexion valide, jeton valable encore %d jour(s). "+
			"LinkedIn ne renouvelle pas les jetons automatiquement : il faudra vous reconnecter avant l'échéance.",
			status.DaysRemaining)
	}

	if !status.Healthy {
		if link, err := s.ReconnectURL(ctx); err == nil {
			status.ReconnectionURL = link
		}
	}
	return status, nil
}
