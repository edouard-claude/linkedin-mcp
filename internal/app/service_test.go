package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/config"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// newHarness wires a Service over two members: A can publish, comment and
// read its analytics, B granted nothing but the identity scopes.
func newHarness(t *testing.T) (*Service, *fakeStore, *fakeAPI, *fakeClock) {
	t.Helper()
	store, api, clk := newFakeStore(), newFakeAPI(), newFakeClock()
	store.seedTenant("tenant-a", "member-a",
		config.ScopeWritePosts, config.ScopeWriteFeed, config.ScopeReadPosts,
		config.ScopeAnalytics)
	store.seedTenant("tenant-b", "member-b", "openid", "profile")
	svc := NewService(store, api, clk, "https://li.example.re",
		[]string{"openid", config.ScopeWritePosts, config.ScopeWriteFeed})
	return svc, store, api, clk
}

func TestPublishPreviewTouchesNothing(t *testing.T) {
	svc, store, api, _ := newHarness(t)

	out, err := svc.PublishPost(t.Context(), "tenant-a", PublishInput{Commentary: "Bonjour"})
	if err != nil {
		t.Fatalf("PublishPost: %v", err)
	}
	if !out.Preview || out.PostURN != "" || out.Notice == "" {
		t.Fatalf("aperçu = %+v", out)
	}
	if api.called("CreatePost") {
		t.Fatal("LinkedIn appelé sans confirmation")
	}
	if posts, _ := store.LedgerPosts(t.Context(), "tenant-a", true, 10); len(posts) != 0 {
		t.Fatalf("registre alimenté sans confirmation: %+v", posts)
	}
}

func TestPublishConfirmedRecordsInTheLedger(t *testing.T) {
	svc, store, api, _ := newHarness(t)

	out, err := svc.PublishPost(t.Context(), "tenant-a", PublishInput{
		Commentary: "Bonjour", Confirm: true,
	})
	if err != nil {
		t.Fatalf("PublishPost: %v", err)
	}
	if out.Preview || out.PostURN != "urn:li:share:1000" {
		t.Fatalf("résultat = %+v", out)
	}
	if out.Permalink == "" || !strings.Contains(out.Permalink, out.PostURN) {
		t.Fatalf("permalien = %q", out.Permalink)
	}
	// The author must be the member, never anything the caller supplied.
	if api.lastActor != "urn:li:person:member-a" {
		t.Fatalf("auteur = %q", api.lastActor)
	}

	// The ledger is what makes list_posts work without r_member_social.
	posts, err := store.LedgerPosts(t.Context(), "tenant-a", false, 10)
	if err != nil || len(posts) != 1 || posts[0].PostURN != out.PostURN {
		t.Fatalf("registre = %+v (err %v)", posts, err)
	}
}

func TestPublishValidation(t *testing.T) {
	svc, _, api, _ := newHarness(t)

	cases := map[string]PublishInput{
		"texte vide":           {},
		"visibilité inconnue":  {Commentary: "x", Visibility: "SECRET"},
		"lien non https":       {Commentary: "x", ArticleURL: "http://exemple.re"},
		"article et repartage": {Commentary: "x", ArticleURL: "https://exemple.re", ReshareOf: "urn:li:share:1"},
		"repartage sans urn":   {Commentary: "x", ReshareOf: "pas-une-urn"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			in.Confirm = true
			if _, err := svc.PublishPost(t.Context(), "tenant-a", in); err == nil {
				t.Fatal("entrée invalide acceptée")
			}
		})
	}
	if api.called("CreatePost") {
		t.Fatal("LinkedIn appelé malgré une entrée invalide")
	}
}

// TestScopeGatingExplainsItself checks the server refuses early, with a
// message that names the permission, rather than passing a doomed request to
// LinkedIn and relaying a bare 403.
func TestScopeGatingExplainsItself(t *testing.T) {
	svc, store, api, _ := newHarness(t)

	_, err := svc.PublishPost(t.Context(), "tenant-b", PublishInput{Commentary: "x", Confirm: true})
	var scopeErr *domain.ErrScopeMissing
	if !errors.As(err, &scopeErr) || scopeErr.Scope != config.ScopeWritePosts {
		t.Fatalf("erreur = %v", err)
	}
	if _, err := svc.PublishComment(t.Context(), "tenant-b",
		CommentInput{ObjectURN: "urn:li:share:1", Message: "x", Confirm: true}); !errors.As(err, &scopeErr) {
		t.Fatalf("erreur = %v", err)
	}
	if _, err := svc.Analytics(t.Context(), "tenant-b", AnalyticsInput{}); !errors.As(err, &scopeErr) {
		t.Fatalf("erreur = %v", err)
	}
	if api.called("CreatePost") || api.called("CreateComment") || api.called("PostAnalytics") {
		t.Fatal("LinkedIn appelé sans la permission")
	}

	// The day-one account: w_member_social publishes, and nothing else. The
	// live API answers 403 on the rest, so the server must say so first, and
	// name the permission rather than relay an opaque refusal.
	store.seedTenant("tenant-jour1", "member-jour1", config.ScopeWritePosts)
	if _, err := svc.PublishPost(t.Context(), "tenant-jour1",
		PublishInput{Commentary: "ça marche", Confirm: true}); err != nil {
		t.Fatalf("publication refusée à un compte qui en a le droit: %v", err)
	}
	for name, run := range map[string]func() error{
		"engagement": func() error { _, e := svc.PostEngagement(t.Context(), "tenant-jour1", "urn:li:share:1"); return e },
		"commentaires": func() error {
			_, e := svc.PostComments(t.Context(), "tenant-jour1", CommentsInput{ObjectURN: "urn:li:share:1"})
			return e
		},
		"commenter": func() error {
			_, e := svc.PublishComment(t.Context(), "tenant-jour1",
				CommentInput{ObjectURN: "urn:li:share:1", Message: "x", Confirm: true})
			return e
		},
		"reagir": func() error {
			_, e := svc.React(t.Context(), "tenant-jour1",
				ReactInput{ObjectURN: "urn:li:share:1", Confirm: true})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if !errors.As(err, &scopeErr) {
				t.Fatalf("erreur = %v", err)
			}
			if !strings.Contains(scopeErr.Error(), "Community Management") {
				t.Fatalf("la voie de sortie n'est pas expliquée: %v", scopeErr)
			}
		})
	}
}

// TestPostsAreScopedToTheirTenant is the guarantee the whole design exists
// for: knowing another member's post URN must not be enough to touch it.
func TestPostsAreScopedToTheirTenant(t *testing.T) {
	svc, store, api, _ := newHarness(t)
	store.seedTenant("tenant-c", "member-c", config.ScopeWritePosts)
	_ = store.RecordPost(t.Context(), "tenant-c", domain.LedgerPost{
		PostURN: "urn:li:share:secret", CreatedAt: time.Now(),
	})

	for name, run := range map[string]func() error{
		"edit": func() error {
			_, e := svc.EditPost(t.Context(), "tenant-a", EditInput{PostURN: "urn:li:share:secret", Commentary: "pirate", Confirm: true})
			return e
		},
		"delete": func() error {
			_, e := svc.DeletePost(t.Context(), "tenant-a", DeleteInput{PostURN: "urn:li:share:secret", Confirm: true})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("action acceptée sur la publication d'un autre membre")
			}
			if !strings.Contains(err.Error(), "inconnue de ce compte") {
				t.Fatalf("message = %v", err)
			}
		})
	}
	if api.called("UpdatePostCommentary") || api.called("DeletePost") {
		t.Fatal("LinkedIn appelé pour un autre membre")
	}
}

func TestEditAndDeleteLifecycle(t *testing.T) {
	svc, store, api, _ := newHarness(t)
	out, err := svc.PublishPost(t.Context(), "tenant-a", PublishInput{Commentary: "v1", Confirm: true})
	if err != nil {
		t.Fatalf("PublishPost: %v", err)
	}

	edit, err := svc.EditPost(t.Context(), "tenant-a", EditInput{PostURN: out.PostURN, Commentary: "v2"})
	if err != nil {
		t.Fatalf("EditPost: %v", err)
	}
	if !edit.Preview || edit.OldCommentary != "v1" || edit.NewCommentary != "v2" {
		t.Fatalf("aperçu = %+v", edit)
	}
	if api.called("UpdatePostCommentary") {
		t.Fatal("modification envoyée sans confirmation")
	}

	if _, err = svc.EditPost(t.Context(), "tenant-a",
		EditInput{PostURN: out.PostURN, Commentary: "v2", Confirm: true}); err != nil {
		t.Fatalf("EditPost: %v", err)
	}
	stored, _ := store.LedgerPost(t.Context(), "tenant-a", out.PostURN)
	if stored.Commentary != "v2" {
		t.Fatalf("registre non mis à jour: %+v", stored)
	}

	del, err := svc.DeletePost(t.Context(), "tenant-a", DeleteInput{PostURN: out.PostURN, Confirm: true})
	if err != nil || !del.Deleted {
		t.Fatalf("DeletePost: %+v (err %v)", del, err)
	}
	// A deleted post can no longer be edited, and the ledger remembers it.
	if _, err := svc.EditPost(t.Context(), "tenant-a",
		EditInput{PostURN: out.PostURN, Commentary: "v3", Confirm: true}); err == nil {
		t.Fatal("modification d'une publication supprimée acceptée")
	}
	if posts, _ := svc.ListPosts(t.Context(), "tenant-a", ListPostsInput{}); len(posts.Posts) != 0 {
		t.Fatalf("publication supprimée encore listée: %+v", posts.Posts)
	}
}

// TestListPostsFallsBackToTheLedger covers the permission LinkedIn withholds:
// without r_member_social the listing comes from what we published, and says so.
func TestListPostsFallsBackToTheLedger(t *testing.T) {
	svc, store, api, _ := newHarness(t)
	// Only the self-serve write scope: the case every account starts in.
	store.seedTenant("tenant-w", "member-w", config.ScopeWritePosts)
	if _, err := svc.PublishPost(t.Context(), "tenant-w",
		PublishInput{Commentary: "a", Confirm: true}); err != nil {
		t.Fatalf("PublishPost: %v", err)
	}

	out, err := svc.ListPosts(t.Context(), "tenant-w", ListPostsInput{})
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if out.Source != domain.SourceLedger || len(out.Posts) != 1 {
		t.Fatalf("sortie = %+v", out)
	}
	if out.Notice == "" || !strings.Contains(out.Notice, "r_member_social") {
		t.Fatalf("la limite n'est pas expliquée: %q", out.Notice)
	}
	if api.called("FindPostsByAuthor") {
		t.Fatal("LinkedIn interrogé sans la permission de lecture")
	}
}

// TestListPostsUsesLinkedInWhenAllowed covers the other branch.
func TestListPostsUsesLinkedInWhenAllowed(t *testing.T) {
	svc, store, api, _ := newHarness(t)
	store.seedTenant("tenant-r", "member-r", config.ScopeReadPosts)
	api.posts = []domain.Post{{URN: "urn:li:share:9", Commentary: "écrit ailleurs"}}

	out, err := svc.ListPosts(t.Context(), "tenant-r", ListPostsInput{})
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if out.Source != domain.SourceLinkedIn || len(out.Posts) != 1 {
		t.Fatalf("sortie = %+v", out)
	}
	if out.Notice != "" {
		t.Fatalf("avertissement inutile: %q", out.Notice)
	}
}

func TestEngagementAndComments(t *testing.T) {
	svc, _, api, _ := newHarness(t)

	eng, err := svc.PostEngagement(t.Context(), "tenant-a", "urn:li:share:1")
	if err != nil {
		t.Fatalf("PostEngagement: %v", err)
	}
	if eng.Likes != 4 || eng.Comments != 2 || eng.Permalink == "" {
		t.Fatalf("engagement = %+v", eng)
	}

	// An untouched post must not read as an error.
	api.counts = domain.SocialCounts{}
	eng, err = svc.PostEngagement(t.Context(), "tenant-a", "urn:li:share:2")
	if err != nil || eng.Notice == "" {
		t.Fatalf("engagement vide = %+v (err %v)", eng, err)
	}

	if _, err := svc.PostEngagement(t.Context(), "tenant-a", "  "); err == nil {
		t.Fatal("URN vide acceptée")
	}
}

func TestCommentAndReactUseTheMemberAsActor(t *testing.T) {
	svc, _, api, _ := newHarness(t)

	out, err := svc.PublishComment(t.Context(), "tenant-a", CommentInput{
		ObjectURN: "urn:li:share:1", Message: " Merci ", Confirm: true,
	})
	if err != nil {
		t.Fatalf("PublishComment: %v", err)
	}
	if out.Message != "Merci" || out.CommentID != "c1" {
		t.Fatalf("commentaire = %+v", out)
	}
	if api.lastActor != "urn:li:person:member-a" {
		t.Fatalf("acteur = %q", api.lastActor)
	}

	if _, err := svc.React(t.Context(), "tenant-a",
		ReactInput{ObjectURN: "urn:li:share:1", Confirm: true}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if !api.called("Like") {
		t.Fatal("Like non appelé")
	}
	if _, err := svc.React(t.Context(), "tenant-a",
		ReactInput{ObjectURN: "urn:li:share:1", Remove: true, Confirm: true}); err != nil {
		t.Fatalf("React: %v", err)
	}
	if !api.called("Unlike") {
		t.Fatal("Unlike non appelé")
	}
}

func TestAnalyticsDropsMetricsThatHaveNoDailyBreakdown(t *testing.T) {
	svc, _, _, _ := newHarness(t)

	out, err := svc.Analytics(t.Context(), "tenant-a", AnalyticsInput{
		PostURN: "urn:li:share:1",
		Metrics: []string{"IMPRESSION", "MEMBERS_REACHED", "LINK_CLICKS"},
		Daily:   true,
	})
	if err != nil {
		t.Fatalf("Analytics: %v", err)
	}
	if len(out.Insights) != 1 || out.Insights[0].Metric != "IMPRESSION" {
		t.Fatalf("insights = %+v", out.Insights)
	}
	if !strings.Contains(out.Notice, "MEMBERS_REACHED") {
		t.Fatalf("les métriques retirées ne sont pas signalées: %q", out.Notice)
	}

	// Asking only for total-only metrics in daily mode is a dead end, and
	// saying so beats returning an empty answer.
	if _, err := svc.Analytics(t.Context(), "tenant-a", AnalyticsInput{
		Metrics: []string{"LINK_CLICKS"}, Daily: true,
	}); err == nil {
		t.Fatal("demande impossible acceptée")
	}
}

func TestAnalyticsValidation(t *testing.T) {
	svc, _, _, _ := newHarness(t)

	if _, err := svc.Analytics(t.Context(), "tenant-a",
		AnalyticsInput{Metrics: []string{"CLICS"}}); err == nil {
		t.Fatal("métrique inconnue acceptée")
	}
	if _, err := svc.Analytics(t.Context(), "tenant-a",
		AnalyticsInput{Since: "hier"}); err == nil {
		t.Fatal("date invalide acceptée")
	}
	if _, err := svc.Analytics(t.Context(), "tenant-a",
		AnalyticsInput{Since: "2026-09-05", Until: "2026-09-01"}); err == nil {
		t.Fatal("période inversée acceptée")
	}
}

func TestConnectionStatusSpellsOutCapabilities(t *testing.T) {
	svc, _, api, clk := newHarness(t)
	api.status = domain.TokenStatus{
		Active:    true,
		ExpiresAt: clk.Now().Add(40 * 24 * time.Hour),
		Scopes:    []string{"openid", config.ScopeWritePosts, config.ScopeWriteFeed},
	}

	status, err := svc.ConnectionStatus(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("ConnectionStatus: %v", err)
	}
	if !status.Healthy || status.DaysRemaining != 40 {
		t.Fatalf("statut = %+v", status)
	}
	if !status.Capabilities["publier"] || status.Capabilities["lire_ses_publications"] ||
		status.Capabilities["lire_engagement"] {
		t.Fatalf("capacités = %+v", status.Capabilities)
	}
	// The 60 day ceiling is the thing users trip over, so the summary says it.
	if !strings.Contains(status.Summary, "reconnecter") {
		t.Fatalf("résumé = %q", status.Summary)
	}
	if status.ReconnectionURL != "" {
		t.Fatalf("lien de reconnexion inutile: %s", status.ReconnectionURL)
	}
}

func TestConnectionStatusOffersAReconnectLinkWhenBroken(t *testing.T) {
	svc, store, api, _ := newHarness(t)
	api.status = domain.TokenStatus{Active: false, Reason: "EXPIRED"}

	status, err := svc.ConnectionStatus(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("ConnectionStatus: %v", err)
	}
	if status.Healthy || !strings.HasPrefix(status.ReconnectionURL, "https://li.example.re/linkedin/login?state=") {
		t.Fatalf("statut = %+v", status)
	}
	state := strings.TrimPrefix(status.ReconnectionURL, "https://li.example.re/linkedin/login?state=")
	if _, err := store.ConsumeLoginState(t.Context(), state); err != nil {
		t.Fatalf("le lien n'est pas utilisable: %v", err)
	}
}

func TestUnknownTenant(t *testing.T) {
	svc, _, _, _ := newHarness(t)
	if _, err := svc.ListPosts(t.Context(), "inconnu", ListPostsInput{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("erreur = %v", err)
	}
}
