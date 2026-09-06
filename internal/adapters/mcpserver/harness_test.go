package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/adapters/crypto"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/sqlite"
	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/edouard-claude/linkedin-mcp/internal/config"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tokens maps a bearer token to the tenant it authorizes, standing in for the
// real JWT verification.
var tokens = map[string]string{
	"token-a": "tenant-a",
	"token-b": "tenant-b",
}

type testClock struct{}

func (testClock) Now() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

// fakeLinkedIn is a domain.Client that records every call instead of making
// one, so a test can assert that nothing reached LinkedIn.
type fakeLinkedIn struct {
	mu    sync.Mutex
	calls []string

	postURN string
	counts  domain.SocialCounts
	status  domain.TokenStatus
}

var _ domain.Client = (*fakeLinkedIn)(nil)

func newFakeLinkedIn() *fakeLinkedIn {
	return &fakeLinkedIn{
		postURN: "urn:li:share:1000",
		counts:  domain.SocialCounts{Likes: 3, Comments: 1},
		status: domain.TokenStatus{
			Active:    true,
			ExpiresAt: testClock{}.Now().Add(45 * 24 * time.Hour),
			Scopes:    []string{"openid", config.ScopeWritePosts, config.ScopeWriteFeed},
		},
	}
}

func (f *fakeLinkedIn) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeLinkedIn) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLinkedIn) AuthorizeURL(redirectURI, state string) string {
	return "https://www.linkedin.com/oauth/v2/authorization?state=" + state
}

func (f *fakeLinkedIn) ExchangeCode(context.Context, string, string) (domain.Token, error) {
	f.record("ExchangeCode")
	return domain.Token{AccessToken: "AQV"}, nil
}

func (f *fakeLinkedIn) RefreshAccessToken(context.Context, string) (domain.Token, error) {
	return domain.Token{}, domain.ErrRefreshUnavailable
}

func (f *fakeLinkedIn) IntrospectToken(context.Context, string) (domain.TokenStatus, error) {
	f.record("IntrospectToken")
	return f.status, nil
}

func (f *fakeLinkedIn) Me(context.Context, string) (domain.Member, error) {
	f.record("Me")
	return domain.Member{ID: "member-a", Name: "Édouard"}, nil
}

func (f *fakeLinkedIn) CreatePost(context.Context, string, string, domain.PublishRequest) (string, error) {
	f.record("CreatePost")
	return f.postURN, nil
}

func (f *fakeLinkedIn) UpdatePostCommentary(context.Context, string, string, string) error {
	f.record("UpdatePostCommentary")
	return nil
}

func (f *fakeLinkedIn) DeletePost(context.Context, string, string) error {
	f.record("DeletePost")
	return nil
}

func (f *fakeLinkedIn) GetPost(context.Context, string, string) (domain.Post, error) {
	f.record("GetPost")
	return domain.Post{URN: f.postURN}, nil
}

func (f *fakeLinkedIn) FindPostsByAuthor(context.Context, string, string, int) ([]domain.Post, error) {
	f.record("FindPostsByAuthor")
	return nil, nil
}

func (f *fakeLinkedIn) SocialCounts(context.Context, string, string) (domain.SocialCounts, error) {
	f.record("SocialCounts")
	return f.counts, nil
}

func (f *fakeLinkedIn) Comments(context.Context, string, string, int) ([]domain.Comment, error) {
	f.record("Comments")
	return []domain.Comment{{CommentID: "c1", Message: "Bravo"}}, nil
}

func (f *fakeLinkedIn) CreateComment(_ context.Context, _, objectURN, _, message, _ string) (domain.Comment, error) {
	f.record("CreateComment")
	return domain.Comment{CommentID: "c1", ObjectURN: objectURN, Message: message}, nil
}

func (f *fakeLinkedIn) DeleteComment(context.Context, string, string, string, string) error {
	f.record("DeleteComment")
	return nil
}

func (f *fakeLinkedIn) Like(context.Context, string, string, string) error {
	f.record("Like")
	return nil
}

func (f *fakeLinkedIn) Unlike(context.Context, string, string, string) error {
	f.record("Unlike")
	return nil
}

func (f *fakeLinkedIn) PostAnalytics(context.Context, string, domain.AnalyticsQuery) (domain.InsightSet, error) {
	f.record("PostAnalytics")
	return domain.InsightSet{Insights: []domain.Insight{{
		Metric: "IMPRESSION",
		Values: []domain.InsightValue{{Value: json.RawMessage("120")}},
	}}}, nil
}

// bearerTransport adds the Authorization header to every client request.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(clone)
}

type serverHarness struct {
	store *sqlite.Store
	api   *fakeLinkedIn
	url   string
}

func newServerHarness(t *testing.T) *serverHarness {
	t.Helper()
	cipher, err := crypto.New(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "mcp.db"), cipher)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	now := testClock{}.Now()
	seed := func(tenantID, memberID string, scopes ...string) {
		t.Helper()
		if err := store.UpsertTenant(t.Context(), &domain.Tenant{
			ID: tenantID, MemberID: memberID, DisplayName: tenantID,
			AccessToken: "AQV-" + tenantID, TokenExpiresAt: now.Add(45 * 24 * time.Hour),
			Scopes: scopes, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertTenant: %v", err)
		}
	}
	seed("tenant-a", "member-a", "openid", config.ScopeWritePosts, config.ScopeWriteFeed)
	seed("tenant-b", "member-b", "openid", config.ScopeWritePosts, config.ScopeWriteFeed)

	// Each tenant owns one post, which is what makes the isolation checks
	// meaningful: the URN exists, it simply belongs to somebody else.
	for tenantID, urn := range map[string]string{
		"tenant-a": "urn:li:share:aaa",
		"tenant-b": "urn:li:share:bbb",
	} {
		if err := store.RecordPost(t.Context(), tenantID, domain.LedgerPost{
			PostURN: urn, Commentary: "texte de " + tenantID, CreatedAt: now,
		}); err != nil {
			t.Fatalf("RecordPost: %v", err)
		}
	}

	api := newFakeLinkedIn()
	svc := app.NewService(store, api, testClock{}, "https://li.example.re",
		[]string{"openid", config.ScopeWritePosts, config.ScopeWriteFeed})
	handler := Handler(
		New(svc, slog.New(slog.DiscardHandler)),
		func(token string) (string, time.Time, error) {
			tenant, ok := tokens[token]
			if !ok {
				return "", time.Time{}, errors.New("jeton inconnu")
			}
			return tenant, time.Now().Add(time.Hour), nil
		},
		HandlerOptions{ResourceMetadataURL: "https://li.example.re/.well-known/oauth-protected-resource"},
		slog.New(slog.DiscardHandler),
	)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &serverHarness{store: store, api: api, url: srv.URL}
}

// connect opens an MCP session authenticated with the given bearer token.
func (h *serverHarness) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             h.url,
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connexion MCP: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call runs a tool and returns its single text block.
func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("CallTool %s: aucun contenu", name)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool %s: contenu de type %T", name, res.Content[0])
	}
	return text.Text, res.IsError
}

func decodeJSON[T any](t *testing.T, payload string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("décodage %q: %v", payload, err)
	}
	return out
}
