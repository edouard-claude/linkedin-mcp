package linkedin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

func TestAuthorizeURL(t *testing.T) {
	f := newFakeAPI(t)
	raw := f.newTestClient().AuthorizeURL("https://mcp.example.re/linkedin/callback", "st")

	if !strings.Contains(raw, "/oauth/v2/authorization?") {
		t.Fatalf("URL = %s", raw)
	}
	for _, want := range []string{
		"response_type=code", "client_id=client-id", "state=st",
		"redirect_uri=https%3A%2F%2Fmcp.example.re%2Flinkedin%2Fcallback",
		// The scope list is space delimited, so it must survive encoding.
		"scope=openid+profile+w_member_social",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("paramètre manquant %q dans %s", want, raw)
		}
	}
}

func TestExchangeCode(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /oauth/v2/accessToken",
		`{"access_token":"AT","expires_in":5184000,"scope":"openid profile w_member_social"}`)

	token, err := f.newTestClient().ExchangeCode(t.Context(), "the-code", "https://cb.test/x")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if token.AccessToken != "AT" || token.ExpiresIn != 5184000*time.Second {
		t.Fatalf("jeton = %+v", token)
	}
	if len(token.Scopes) != 3 || token.Scopes[2] != "w_member_social" {
		t.Fatalf("scopes = %v", token.Scopes)
	}

	form := f.calls("/oauth/v2/accessToken")[0].Form
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" {
		t.Fatalf("formulaire = %v", form)
	}
	if form.Get("client_secret") != "client-secret" {
		t.Fatal("le secret client n'a pas été envoyé")
	}
}

// TestRefreshWithoutTokenIsReported pins the LinkedIn reality: most apps get
// no refresh token, and the caller must be able to tell that apart from a
// transport failure.
func TestRefreshWithoutTokenIsReported(t *testing.T) {
	f := newFakeAPI(t)
	if _, err := f.newTestClient().RefreshAccessToken(t.Context(), ""); err != domain.ErrRefreshUnavailable {
		t.Fatalf("erreur = %v", err)
	}
}

func TestMe(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v2/userinfo",
		`{"sub":"abc123","name":"Édouard Claude","email":"e@x.test","locale":{"country":"FR","language":"fr"}}`)

	member, err := f.newTestClient().Me(t.Context(), "AT")
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if member.ID != "abc123" || member.Name != "Édouard Claude" || member.Locale != "fr_FR" {
		t.Fatalf("membre = %+v", member)
	}

	// userinfo is not a versioned resource: sending the version header there
	// is what makes LinkedIn answer 426.
	if v := f.calls("/v2/userinfo")[0].Header.Get("LinkedIn-Version"); v != "" {
		t.Fatalf("en-tête de version envoyé à userinfo: %q", v)
	}
}

func TestCreatePostReadsTheURNFromTheHeader(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /rest/posts", func(w http.ResponseWriter, r *http.Request) {
		// LinkedIn returns the new URN in a header, not in the body.
		w.Header().Set("x-restli-id", "urn:li:share:7325786486870552578")
		w.WriteHeader(http.StatusCreated)
	})

	urn, err := f.newTestClient().CreatePost(t.Context(), "AT", "urn:li:person:abc",
		domain.PublishRequest{Commentary: "Bonjour", Visibility: domain.VisibilityPublic})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if urn != "urn:li:share:7325786486870552578" {
		t.Fatalf("urn = %q", urn)
	}

	call := f.calls("/rest/posts")[0]
	if call.Header.Get("LinkedIn-Version") != "202606" {
		t.Fatalf("version = %q", call.Header.Get("LinkedIn-Version"))
	}
	if call.Header.Get("X-Restli-Protocol-Version") != "2.0.0" {
		t.Fatal("en-tête Restli absent")
	}
	for _, want := range []string{`"author":"urn:li:person:abc"`, `"commentary":"Bonjour"`,
		`"lifecycleState":"PUBLISHED"`, `"feedDistribution":"MAIN_FEED"`} {
		if !strings.Contains(call.Body, want) {
			t.Errorf("corps sans %s: %s", want, call.Body)
		}
	}
}

func TestCreatePostWithoutURNFails(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /rest/posts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	if _, err := f.newTestClient().CreatePost(t.Context(), "AT", "urn:li:person:abc",
		domain.PublishRequest{Commentary: "x"}); err == nil {
		t.Fatal("une réponse sans identifiant a été acceptée")
	}
}

func TestCreateArticleAndReshare(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /rest/posts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-restli-id", "urn:li:share:1")
		w.WriteHeader(http.StatusCreated)
	})
	c := f.newTestClient()

	if _, err := c.CreatePost(t.Context(), "AT", "urn:li:person:abc", domain.PublishRequest{
		Commentary: "lis ça", ArticleURL: "https://exemple.re/a", ArticleTitle: "Titre",
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if body := f.calls("/rest/posts")[0].Body; !strings.Contains(body, `"article"`) ||
		!strings.Contains(body, `"source":"https://exemple.re/a"`) {
		t.Fatalf("corps article = %s", body)
	}

	if _, err := c.CreatePost(t.Context(), "AT", "urn:li:person:abc", domain.PublishRequest{
		Commentary: "d'accord", ReshareOf: "urn:li:share:999",
	}); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if body := f.calls("/rest/posts")[1].Body; !strings.Contains(body, `"reshareContext":{"parent":"urn:li:share:999"}`) {
		t.Fatalf("corps repartage = %s", body)
	}
}

func TestUpdateAndDeletePost(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /rest/posts/urn:li:share:1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	f.handle("DELETE /rest/posts/urn:li:share:1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := f.newTestClient()

	if err := c.UpdatePostCommentary(t.Context(), "AT", "urn:li:share:1", "corrigé"); err != nil {
		t.Fatalf("UpdatePostCommentary: %v", err)
	}
	call := f.calls("/rest/posts/urn:li:share:1")[0]
	if call.Header.Get("X-RestLi-Method") != "PARTIAL_UPDATE" {
		t.Fatalf("méthode Restli = %q", call.Header.Get("X-RestLi-Method"))
	}
	if !strings.Contains(call.Body, `"$set":{"commentary":"corrigé"}`) {
		t.Fatalf("corps = %s", call.Body)
	}

	if err := c.DeletePost(t.Context(), "AT", "urn:li:share:1"); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}
}

// TestSocialCountsTreatsNotFoundAsZero pins a LinkedIn quirk: a post nobody
// has touched answers 404 rather than an empty summary.
func TestSocialCountsTreatsNotFoundAsZero(t *testing.T) {
	f := newFakeAPI(t)
	f.fail("GET /rest/socialActions/urn:li:share:1", http.StatusNotFound, "not found")

	counts, err := f.newTestClient().SocialCounts(t.Context(), "AT", "urn:li:share:1")
	if err != nil {
		t.Fatalf("SocialCounts: %v", err)
	}
	if counts.Likes != 0 || counts.Comments != 0 || counts.ObjectURN != "urn:li:share:1" {
		t.Fatalf("compteurs = %+v", counts)
	}
}

func TestSocialCounts(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /rest/socialActions/urn:li:share:1", `{
		"target":"urn:li:share:1",
		"likesSummary":{"totalLikes":12,"aggregatedTotalLikes":12,"likedByCurrentUser":true},
		"commentsSummary":{"totalFirstLevelComments":3,"aggregatedTotalComments":5,"commentsState":"OPEN"}}`)

	counts, err := f.newTestClient().SocialCounts(t.Context(), "AT", "urn:li:share:1")
	if err != nil {
		t.Fatalf("SocialCounts: %v", err)
	}
	// Aggregated counts include replies, so the larger number is the honest
	// answer to "how many comments does this have".
	if counts.Likes != 12 || counts.Comments != 5 || !counts.LikedByMe {
		t.Fatalf("compteurs = %+v", counts)
	}
}

func TestCommentsAndCreateComment(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /rest/socialActions/urn:li:share:1/comments", `{"elements":[
		{"id":"71","commentUrn":"urn:li:comment:(urn:li:share:1,71)","actor":"urn:li:person:x",
		 "object":"urn:li:share:1","message":{"text":"Bravo"},"created":{"time":1757000000000},
		 "likesSummary":{"totalLikes":2}}]}`)
	f.json("POST /rest/socialActions/urn:li:share:1/comments", `{
		"id":"99","commentUrn":"urn:li:comment:(urn:li:share:1,99)","actor":"urn:li:person:me",
		"object":"urn:li:share:1","message":{"text":"Merci"}}`)
	c := f.newTestClient()

	comments, err := c.Comments(t.Context(), "AT", "urn:li:share:1", 25)
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Message != "Bravo" || comments[0].LikeCount != 2 {
		t.Fatalf("commentaires = %+v", comments)
	}
	if comments[0].CreatedAt == "" {
		t.Fatal("date non convertie depuis les millisecondes")
	}

	created, err := c.CreateComment(t.Context(), "AT", "urn:li:share:1", "urn:li:person:me", "Merci", "")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if created.CommentID != "99" {
		t.Fatalf("commentaire = %+v", created)
	}
	if body := f.calls("/rest/socialActions/urn:li:share:1/comments")[1].Body; !strings.Contains(body, `"actor":"urn:li:person:me"`) {
		t.Fatalf("corps = %s", body)
	}
}

// TestCommentsOnEmptyPostIsNotAnError covers the same 404 quirk on the
// comments edge.
func TestCommentsOnEmptyPostIsNotAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.fail("GET /rest/socialActions/urn:li:share:1/comments", http.StatusNotFound, "not found")

	comments, err := f.newTestClient().Comments(t.Context(), "AT", "urn:li:share:1", 25)
	if err != nil || len(comments) != 0 {
		t.Fatalf("commentaires = %+v, err = %v", comments, err)
	}
}

func TestReplyTargetsTheParentComment(t *testing.T) {
	f := newFakeAPI(t)
	parent := "urn:li:comment:(urn:li:share:1,71)"
	f.json("POST /rest/socialActions/"+parent+"/comments", `{"id":"100","message":{"text":"ok"}}`)

	if _, err := f.newTestClient().CreateComment(t.Context(), "AT",
		"urn:li:share:1", "urn:li:person:me", "ok", parent); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if body := f.calls("/rest/socialActions/" + parent + "/comments")[0].Body; !strings.Contains(body, `"parentComment"`) {
		t.Fatalf("corps = %s", body)
	}
}

func TestLikeAndUnlike(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /rest/socialActions/urn:li:share:1/likes", `{}`)
	f.handle("DELETE /rest/socialActions/urn:li:share:1/likes/urn:li:person:me",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	c := f.newTestClient()

	if err := c.Like(t.Context(), "AT", "urn:li:share:1", "urn:li:person:me"); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if err := c.Unlike(t.Context(), "AT", "urn:li:share:1", "urn:li:person:me"); err != nil {
		t.Fatalf("Unlike: %v", err)
	}
	// The actor must travel as a query parameter too, or LinkedIn refuses.
	calls := f.calls("/rest/socialActions/urn:li:share:1/likes/urn:li:person:me")
	if calls[0].Query.Get("actor") != "urn:li:person:me" {
		t.Fatalf("query = %v", calls[0].Query)
	}
}

func TestAuthErrorsAreTyped(t *testing.T) {
	f := newFakeAPI(t)
	f.fail("GET /v2/userinfo", http.StatusUnauthorized, "Invalid access token")

	_, err := f.newTestClient().Me(t.Context(), "expired")
	ae, ok := domain.AsAPIError(err)
	if !ok {
		t.Fatalf("erreur non typée: %v", err)
	}
	if !ae.IsAuth() || !strings.Contains(ae.UserMessage(), "reconnect_url") {
		t.Fatalf("erreur = %+v, message = %q", ae, ae.UserMessage())
	}
}

func TestRateLimitRetriesOnce(t *testing.T) {
	f := newFakeAPI(t)
	attempts := 0
	f.handle("GET /v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"throttled","status":429}`))
			return
		}
		_, _ = w.Write([]byte(`{"sub":"abc","name":"X"}`))
	})

	if _, err := f.newTestClient().Me(t.Context(), "AT"); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("%d tentatives, attendu 2", attempts)
	}
}

// TestAnalyticsAsksOneMetricAtATime pins the shape the API imposes: queryType
// takes a single metric, so a set of metrics is a set of requests.
func TestAnalyticsAsksOneMetricAtATime(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /rest/memberCreatorPostAnalytics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("queryType") == "POST_SAVE" {
			// An unsupported combination fails the request, and must not
			// take the other metrics down with it.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Unsupported query type","status":400}`))
			return
		}
		_, _ = w.Write([]byte(`{"elements":[{"count":42,"metricType":"IMPRESSION"}]}`))
	})

	set, err := f.newTestClient().PostAnalytics(t.Context(), "AT", domain.AnalyticsQuery{
		PostURN:     "urn:li:share:1",
		Metrics:     []string{"IMPRESSION", "POST_SAVE", "REACTION"},
		Aggregation: domain.AggregationTotal,
	})
	if err != nil {
		t.Fatalf("PostAnalytics: %v", err)
	}
	if len(set.Insights) != 2 {
		t.Fatalf("métriques retenues = %+v", set.Insights)
	}
	if len(set.Rejected) != 1 || set.Rejected[0] != "POST_SAVE" {
		t.Fatalf("rejected = %v", set.Rejected)
	}

	calls := f.calls("/rest/memberCreatorPostAnalytics")
	if len(calls) != 3 {
		t.Fatalf("%d appels, attendu un par métrique", len(calls))
	}
	if q := calls[0].Query.Get("q"); q != "entity" {
		t.Fatalf("finder = %q", q)
	}
	// A share URN is wrapped as (share:...), a ugcPost as (ugc:...).
	if e := calls[0].Query.Get("entity"); !strings.HasPrefix(e, "(share:") {
		t.Fatalf("entity = %q", e)
	}
}

func TestAnalyticsAggregateUsesTheMeFinder(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /rest/memberCreatorPostAnalytics", `{"elements":[{"count":7,"metricType":"REACTION",
		"dateRange":{"start":{"year":2026,"month":9,"day":1},"end":{"year":2026,"month":9,"day":2}}}]}`)

	set, err := f.newTestClient().PostAnalytics(t.Context(), "AT", domain.AnalyticsQuery{
		Metrics:     []string{"REACTION"},
		Aggregation: domain.AggregationDaily,
		Since:       time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Until:       time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("PostAnalytics: %v", err)
	}
	if len(set.Insights) != 1 || len(set.Insights[0].Values) != 1 {
		t.Fatalf("insights = %+v", set.Insights)
	}
	if set.Insights[0].Values[0].Start != "2026-09-01" {
		t.Fatalf("date = %q", set.Insights[0].Values[0].Start)
	}

	q := f.calls("/rest/memberCreatorPostAnalytics")[0].Query
	if q.Get("q") != "me" || q.Get("entity") != "" {
		t.Fatalf("finder agrégé mal formé: %v", q)
	}
	if want := "(start:(day:1,month:9,year:2026),end:(day:5,month:9,year:2026))"; q.Get("dateRange") != want {
		t.Fatalf("dateRange = %q", q.Get("dateRange"))
	}
}

// TestAnalyticsSurfacesMissingPermission keeps the 403 from being swallowed
// as "metric rejected": the member has to know the scope is missing.
func TestAnalyticsSurfacesMissingPermission(t *testing.T) {
	f := newFakeAPI(t)
	f.fail("GET /rest/memberCreatorPostAnalytics", http.StatusForbidden, "Not enough permissions")

	_, err := f.newTestClient().PostAnalytics(t.Context(), "AT", domain.AnalyticsQuery{
		PostURN: "urn:li:share:1", Metrics: []string{"IMPRESSION"},
	})
	ae, ok := domain.AsAPIError(err)
	if !ok || ae.HTTPStatus != http.StatusForbidden {
		t.Fatalf("erreur = %v", err)
	}
}
