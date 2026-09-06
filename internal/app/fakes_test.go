package app

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// fakeClock is a controllable domain.Clock.
type fakeClock struct{ now time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

// fakeStore is an in-memory domain.TenantStore. It enforces the same tenant
// scoping as the SQLite implementation, so a use case that leaks across
// tenants fails here too.
type fakeStore struct {
	mu       sync.Mutex
	tenants  map[string]*domain.Tenant
	posts    map[string]map[string]*domain.LedgerPost
	clients  map[string]*domain.OAuthClient
	codes    map[string]*domain.AuthCode
	refresh  map[string]*domain.RefreshToken
	states   map[string]*domain.LoginState
	failures map[string]error
}

var _ domain.TenantStore = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenants:  map[string]*domain.Tenant{},
		posts:    map[string]map[string]*domain.LedgerPost{},
		clients:  map[string]*domain.OAuthClient{},
		codes:    map[string]*domain.AuthCode{},
		refresh:  map[string]*domain.RefreshToken{},
		states:   map[string]*domain.LoginState{},
		failures: map[string]error{},
	}
}

func (s *fakeStore) failOn(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[method] = err
}

func (s *fakeStore) UpsertTenant(_ context.Context, t *domain.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["UpsertTenant"]; err != nil {
		return err
	}
	clone := *t
	s.tenants[t.ID] = &clone
	return nil
}

func (s *fakeStore) TenantByID(_ context.Context, id string) (*domain.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tenants[id]; ok {
		clone := *t
		return &clone, nil
	}
	return nil, domain.ErrNotFound
}

func (s *fakeStore) TenantByMemberID(_ context.Context, memberID string) (*domain.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tenants {
		if t.MemberID == memberID {
			clone := *t
			return &clone, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s *fakeStore) DeleteTenant(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tenants, id)
	delete(s.posts, id)
	return nil
}

func (s *fakeStore) TenantsDueForTokenRefresh(_ context.Context, expiringBefore, uncheckedBefore time.Time) ([]domain.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["TenantsDueForTokenRefresh"]; err != nil {
		return nil, err
	}
	var out []domain.Tenant
	for _, id := range slices.Sorted(maps.Keys(s.tenants)) {
		t := s.tenants[id]
		known := !t.TokenExpiresAt.IsZero()
		if (known && t.TokenExpiresAt.Before(expiringBefore)) ||
			(!known && t.UpdatedAt.Before(uncheckedBefore)) {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (s *fakeStore) RecordPost(_ context.Context, tenantID string, p domain.LedgerPost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["RecordPost"]; err != nil {
		return err
	}
	if s.posts[tenantID] == nil {
		s.posts[tenantID] = map[string]*domain.LedgerPost{}
	}
	clone := p
	s.posts[tenantID][p.PostURN] = &clone
	return nil
}

func (s *fakeStore) MarkPostDeleted(_ context.Context, tenantID, postURN string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.posts[tenantID][postURN]; ok {
		p.DeletedAt = at
	}
	return nil
}

func (s *fakeStore) UpdateLedgerCommentary(_ context.Context, tenantID, postURN, commentary string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.posts[tenantID][postURN]; ok {
		p.Commentary, p.UpdatedAt = commentary, at
	}
	return nil
}

func (s *fakeStore) LedgerPosts(_ context.Context, tenantID string, includeDeleted bool, limit int) ([]domain.LedgerPost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failures["LedgerPosts"]; err != nil {
		return nil, err
	}
	out := []domain.LedgerPost{}
	for _, urn := range slices.Sorted(maps.Keys(s.posts[tenantID])) {
		p := s.posts[tenantID][urn]
		if p.Deleted() && !includeDeleted {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, *p)
	}
	return out, nil
}

func (s *fakeStore) LedgerPost(_ context.Context, tenantID, postURN string) (*domain.LedgerPost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.posts[tenantID][postURN]; ok {
		clone := *p
		return &clone, nil
	}
	return nil, domain.ErrNotFound
}

func (s *fakeStore) RegisterClient(_ context.Context, c *domain.OAuthClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *c
	s.clients[c.ClientID] = &clone
	return nil
}

func (s *fakeStore) ClientByID(_ context.Context, clientID string) (*domain.OAuthClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[clientID]; ok {
		clone := *c
		return &clone, nil
	}
	return nil, domain.ErrNotFound
}

func (s *fakeStore) CreateAuthCode(_ context.Context, c *domain.AuthCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *c
	s.codes[c.Code] = &clone
	return nil
}

func (s *fakeStore) ConsumeAuthCode(_ context.Context, code string) (*domain.AuthCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	if !ok {
		return nil, domain.ErrNotFound
	}
	delete(s.codes, code)
	return c, nil
}

func (s *fakeStore) CreateRefreshToken(_ context.Context, rt *domain.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *rt
	s.refresh[rt.TokenHash] = &clone
	return nil
}

func (s *fakeStore) RotateRefreshToken(_ context.Context, hash string, now time.Time) (*domain.RefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.refresh[hash]
	if !ok || rt.Revoked || !now.Before(rt.ExpiresAt) {
		return nil, domain.ErrNotFound
	}
	rt.Revoked = true
	clone := *rt
	return &clone, nil
}

func (s *fakeStore) RevokeTenantRefreshTokens(_ context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.refresh {
		if rt.TenantID == tenantID {
			rt.Revoked = true
		}
	}
	return nil
}

func (s *fakeStore) CreateLoginState(_ context.Context, st *domain.LoginState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *st
	s.states[st.State] = &clone
	return nil
}

func (s *fakeStore) ConsumeLoginState(_ context.Context, state string) (*domain.LoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[state]
	if !ok {
		return nil, domain.ErrNotFound
	}
	delete(s.states, state)
	return st, nil
}

func (s *fakeStore) PurgeExpired(context.Context, time.Time) error { return nil }
func (s *fakeStore) Ping(context.Context) error                    { return nil }
func (s *fakeStore) Close() error                                  { return nil }

// seedTenant registers a member with the given scopes.
func (s *fakeStore) seedTenant(id, memberID string, scopes ...string) {
	_ = s.UpsertTenant(context.Background(), &domain.Tenant{
		ID: id, MemberID: memberID, DisplayName: "Membre " + id,
		AccessToken: "AT-" + id, Scopes: scopes,
		TokenExpiresAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	})
}

// fakeAPI is a scriptable domain.Client.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string

	member    domain.Member
	token     domain.Token
	status    domain.TokenStatus
	posts     []domain.Post
	comments  []domain.Comment
	counts    domain.SocialCounts
	analytics domain.InsightSet

	createErr    error
	findErr      error
	analyticsErr error
	statusErr    error
	refreshErr   error

	createdURN string
	lastActor  string
	lastTarget string
}

var _ domain.Client = (*fakeAPI)(nil)

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		member:     domain.Member{ID: "member-1", Name: "Édouard"},
		token:      domain.Token{AccessToken: "AT", ExpiresIn: 60 * 24 * time.Hour, Scopes: []string{"openid", "w_member_social"}},
		status:     domain.TokenStatus{Active: true},
		createdURN: "urn:li:share:1000",
		counts:     domain.SocialCounts{Likes: 4, Comments: 2},
		analytics:  domain.InsightSet{Insights: []domain.Insight{{Metric: "IMPRESSION"}}},
		refreshErr: domain.ErrRefreshUnavailable,
	}
}

func (f *fakeAPI) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeAPI) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, name)
}

func (f *fakeAPI) AuthorizeURL(redirectURI, state string) string {
	return "https://linkedin.test/oauth?redirect_uri=" + redirectURI + "&state=" + state
}

func (f *fakeAPI) ExchangeCode(context.Context, string, string) (domain.Token, error) {
	f.record("ExchangeCode")
	return f.token, nil
}

func (f *fakeAPI) RefreshAccessToken(context.Context, string) (domain.Token, error) {
	f.record("RefreshAccessToken")
	if f.refreshErr != nil {
		return domain.Token{}, f.refreshErr
	}
	return f.token, nil
}

func (f *fakeAPI) Me(context.Context, string) (domain.Member, error) {
	f.record("Me")
	return f.member, nil
}

func (f *fakeAPI) IntrospectToken(context.Context, string) (domain.TokenStatus, error) {
	f.record("IntrospectToken")
	return f.status, f.statusErr
}

func (f *fakeAPI) CreatePost(_ context.Context, _, authorURN string, _ domain.PublishRequest) (string, error) {
	f.record("CreatePost")
	f.lastActor = authorURN
	return f.createdURN, f.createErr
}

func (f *fakeAPI) UpdatePostCommentary(_ context.Context, _, postURN, _ string) error {
	f.record("UpdatePostCommentary")
	f.lastTarget = postURN
	return nil
}

func (f *fakeAPI) DeletePost(_ context.Context, _, postURN string) error {
	f.record("DeletePost")
	f.lastTarget = postURN
	return nil
}

func (f *fakeAPI) GetPost(context.Context, string, string) (domain.Post, error) {
	f.record("GetPost")
	return domain.Post{}, errors.New("non implémenté")
}

func (f *fakeAPI) FindPostsByAuthor(context.Context, string, string, int) ([]domain.Post, error) {
	f.record("FindPostsByAuthor")
	return f.posts, f.findErr
}

func (f *fakeAPI) SocialCounts(_ context.Context, _, objectURN string) (domain.SocialCounts, error) {
	f.record("SocialCounts")
	counts := f.counts
	counts.ObjectURN = objectURN
	return counts, nil
}

func (f *fakeAPI) Comments(context.Context, string, string, int) ([]domain.Comment, error) {
	f.record("Comments")
	return f.comments, nil
}

func (f *fakeAPI) CreateComment(_ context.Context, _, objectURN, actorURN, message, parent string) (domain.Comment, error) {
	f.record("CreateComment")
	f.lastActor, f.lastTarget = actorURN, objectURN
	return domain.Comment{CommentID: "c1", CommentURN: "urn:li:comment:(x,c1)", Message: message, ParentURN: parent}, nil
}

func (f *fakeAPI) DeleteComment(_ context.Context, _, objectURN, commentID, actorURN string) error {
	f.record("DeleteComment")
	f.lastActor, f.lastTarget = actorURN, commentID
	return nil
}

func (f *fakeAPI) Like(_ context.Context, _, objectURN, actorURN string) error {
	f.record("Like")
	f.lastActor, f.lastTarget = actorURN, objectURN
	return nil
}

func (f *fakeAPI) Unlike(_ context.Context, _, objectURN, actorURN string) error {
	f.record("Unlike")
	f.lastActor, f.lastTarget = actorURN, objectURN
	return nil
}

func (f *fakeAPI) PostAnalytics(_ context.Context, _ string, q domain.AnalyticsQuery) (domain.InsightSet, error) {
	f.record("PostAnalytics")
	if f.analyticsErr != nil {
		return domain.InsightSet{}, f.analyticsErr
	}
	set := domain.InsightSet{Insights: []domain.Insight{}}
	for _, m := range q.Metrics {
		set.Insights = append(set.Insights, domain.Insight{Metric: m})
	}
	return set, nil
}
