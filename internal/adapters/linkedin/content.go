package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// ----- posts -----

// postBody is the JSON the Posts API takes on create.
type postBody struct {
	Author            string       `json:"author"`
	Commentary        string       `json:"commentary"`
	Visibility        string       `json:"visibility"`
	Distribution      distribution `json:"distribution"`
	LifecycleState    string       `json:"lifecycleState"`
	IsReshareDisabled bool         `json:"isReshareDisabledByAuthor"`
	Content           *content     `json:"content,omitempty"`
	ReshareContext    *reshare     `json:"reshareContext,omitempty"`
}

type distribution struct {
	FeedDistribution               string   `json:"feedDistribution"`
	TargetEntities                 []any    `json:"targetEntities"`
	ThirdPartyDistributionChannels []string `json:"thirdPartyDistributionChannels"`
}

type content struct {
	Article *article `json:"article,omitempty"`
}

type article struct {
	Source      string `json:"source"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type reshare struct {
	Parent string `json:"parent"`
}

// CreatePost publishes a post and returns its URN.
//
// The URN comes back in the x-restli-id response header rather than the body,
// which is why this call reads the headers.
func (c *Client) CreatePost(ctx context.Context, token, authorURN string, req domain.PublishRequest) (string, error) {
	body := postBody{
		Author:            authorURN,
		Commentary:        req.Commentary,
		Visibility:        orDefault(req.Visibility, domain.VisibilityPublic),
		LifecycleState:    "PUBLISHED",
		IsReshareDisabled: req.DisableReshareByOthers,
		Distribution: distribution{
			FeedDistribution:               "MAIN_FEED",
			TargetEntities:                 []any{},
			ThirdPartyDistributionChannels: []string{},
		},
	}
	if req.IsArticle() {
		// LinkedIn does not scrape the URL, so whatever title and
		// description we send is exactly what readers will see.
		body.Content = &content{Article: &article{
			Source:      req.ArticleURL,
			Title:       req.ArticleTitle,
			Description: req.ArticleDescription,
		}}
	}
	if req.IsReshare() {
		body.ReshareContext = &reshare{Parent: req.ReshareOf}
	}

	var header http.Header
	if err := c.do(ctx, request{
		method: http.MethodPost,
		path:   "/rest/posts",
		token:  token,
		body:   body,
		header: &header,
	}); err != nil {
		return "", fmt.Errorf("publication: %w", err)
	}

	urn := header.Get("x-restli-id")
	if urn == "" {
		return "", errors.New("publication: LinkedIn n'a renvoyé aucun identifiant")
	}
	return urn, nil
}

// UpdatePostCommentary edits the text of a post. It is the only field of an
// organic post the API lets us change.
func (c *Client) UpdatePostCommentary(ctx context.Context, token, postURN, commentary string) error {
	body := map[string]any{
		"patch": map[string]any{
			"$set": map[string]any{"commentary": commentary},
		},
	}
	if err := c.do(ctx, request{
		method:       http.MethodPost,
		path:         "/rest/posts/" + encodeURN(postURN),
		token:        token,
		body:         body,
		restliMethod: "PARTIAL_UPDATE",
	}); err != nil {
		return fmt.Errorf("modification de la publication: %w", err)
	}
	return nil
}

// DeletePost removes a post. LinkedIn makes deletion idempotent, so deleting
// twice succeeds.
func (c *Client) DeletePost(ctx context.Context, token, postURN string) error {
	if err := c.do(ctx, request{
		method:       http.MethodDelete,
		path:         "/rest/posts/" + encodeURN(postURN),
		token:        token,
		restliMethod: "DELETE",
	}); err != nil {
		return fmt.Errorf("suppression de la publication: %w", err)
	}
	return nil
}

// rawPost is the Posts API representation.
type rawPost struct {
	ID             string `json:"id"`
	Author         string `json:"author"`
	Commentary     string `json:"commentary"`
	Visibility     string `json:"visibility"`
	LifecycleState string `json:"lifecycleState"`
	CreatedAt      int64  `json:"createdAt"`
	LastModifiedAt int64  `json:"lastModifiedAt"`
	PublishedAt    int64  `json:"publishedAt"`
	LifecycleInfo  struct {
		IsEditedByAuthor bool `json:"isEditedByAuthor"`
	} `json:"lifecycleStateInfo"`
}

func (r rawPost) toPost() domain.Post {
	return domain.Post{
		URN:            r.ID,
		Commentary:     r.Commentary,
		Visibility:     r.Visibility,
		LifecycleState: r.LifecycleState,
		Permalink:      Permalink(r.ID),
		CreatedAt:      millisToRFC3339(r.CreatedAt),
		LastModifiedAt: millisToRFC3339(r.LastModifiedAt),
		EditedByAuthor: r.LifecycleInfo.IsEditedByAuthor,
		Source:         domain.SourceLinkedIn,
	}
}

// millisToRFC3339 turns LinkedIn's epoch milliseconds into a readable date.
func millisToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// Permalink builds the public URL of a post from its URN.
func Permalink(urn string) string {
	if urn == "" {
		return ""
	}
	return "https://www.linkedin.com/feed/update/" + urn + "/"
}

// GetPost reads one post. It needs r_member_social, a restricted permission,
// so most apps get a 403 here.
func (c *Client) GetPost(ctx context.Context, token, postURN string) (domain.Post, error) {
	var raw rawPost
	if err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/rest/posts/" + encodeURN(postURN),
		token:  token,
		query:  url.Values{"viewContext": {"AUTHOR"}},
		out:    &raw,
	}); err != nil {
		return domain.Post{}, fmt.Errorf("lecture de la publication: %w", err)
	}
	return raw.toPost(), nil
}

// FindPostsByAuthor lists the posts of a member. It needs r_member_social,
// a restricted permission; the ledger is the fallback when it is missing.
func (c *Client) FindPostsByAuthor(ctx context.Context, token, authorURN string, limit int) ([]domain.Post, error) {
	var resp struct {
		Elements []rawPost `json:"elements"`
	}
	if err := c.do(ctx, request{
		method:       http.MethodGet,
		path:         "/rest/posts",
		token:        token,
		restliMethod: "FINDER",
		query: url.Values{
			"q":      {"author"},
			"author": {authorURN},
			"count":  {strconv.Itoa(clamp(limit, 1, 100))},
			"sortBy": {"CREATED"},
		},
		out: &resp,
	}); err != nil {
		return nil, fmt.Errorf("liste des publications: %w", err)
	}

	posts := make([]domain.Post, 0, len(resp.Elements))
	for _, raw := range resp.Elements {
		posts = append(posts, raw.toPost())
	}
	return posts, nil
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

// ----- social actions -----

// SocialCounts reads the engagement summary of a post.
//
// LinkedIn answers 404 for a post nobody has touched yet, which is not an
// error: it is reported as zero counts.
func (c *Client) SocialCounts(ctx context.Context, token, objectURN string) (domain.SocialCounts, error) {
	var resp struct {
		Target       string `json:"target"`
		LikesSummary struct {
			TotalLikes         int64 `json:"totalLikes"`
			AggregatedTotal    int64 `json:"aggregatedTotalLikes"`
			LikedByCurrentUser bool  `json:"likedByCurrentUser"`
		} `json:"likesSummary"`
		CommentsSummary struct {
			TotalFirstLevel int64  `json:"totalFirstLevelComments"`
			Aggregated      int64  `json:"aggregatedTotalComments"`
			State           string `json:"commentsState"`
		} `json:"commentsSummary"`
	}
	err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/rest/socialActions/" + encodeURN(objectURN),
		token:  token,
		out:    &resp,
	})
	if err != nil {
		if ae, ok := domain.AsAPIError(err); ok && ae.IsNotFound() {
			return domain.SocialCounts{ObjectURN: objectURN}, nil
		}
		return domain.SocialCounts{}, fmt.Errorf("engagement de la publication: %w", err)
	}

	return domain.SocialCounts{
		ObjectURN:     objectURN,
		Likes:         max64(resp.LikesSummary.TotalLikes, resp.LikesSummary.AggregatedTotal),
		Comments:      max64(resp.CommentsSummary.TotalFirstLevel, resp.CommentsSummary.Aggregated),
		CommentsState: resp.CommentsSummary.State,
		LikedByMe:     resp.LikesSummary.LikedByCurrentUser,
	}, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// rawComment is the socialActions comment representation.
type rawComment struct {
	ID         string `json:"id"`
	CommentURN string `json:"commentUrn"`
	Actor      string `json:"actor"`
	Object     string `json:"object"`
	Parent     string `json:"parentComment"`
	Message    struct {
		Text string `json:"text"`
	} `json:"message"`
	Created struct {
		Time int64 `json:"time"`
	} `json:"created"`
	LikesSummary struct {
		TotalLikes int64 `json:"totalLikes"`
	} `json:"likesSummary"`
	CommentsSummary struct {
		TotalFirstLevel int64 `json:"totalFirstLevelComments"`
	} `json:"commentsSummary"`
}

func (r rawComment) toComment() domain.Comment {
	return domain.Comment{
		CommentURN: r.CommentURN,
		CommentID:  r.ID,
		ActorURN:   r.Actor,
		ObjectURN:  r.Object,
		ParentURN:  r.Parent,
		Message:    r.Message.Text,
		CreatedAt:  millisToRFC3339(r.Created.Time),
		LikeCount:  r.LikesSummary.TotalLikes,
		ReplyCount: r.CommentsSummary.TotalFirstLevel,
	}
}

// Comments lists the comments on a post or on another comment.
func (c *Client) Comments(ctx context.Context, token, objectURN string, limit int) ([]domain.Comment, error) {
	var resp struct {
		Elements []rawComment `json:"elements"`
	}
	err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/rest/socialActions/" + encodeURN(objectURN) + "/comments",
		token:  token,
		query:  url.Values{"count": {strconv.Itoa(clamp(limit, 1, 100))}},
		out:    &resp,
	})
	if err != nil {
		// A post with no comments at all answers 404 rather than an empty
		// list, which the caller should read as "nothing yet".
		if ae, ok := domain.AsAPIError(err); ok && ae.IsNotFound() {
			return []domain.Comment{}, nil
		}
		return nil, fmt.Errorf("commentaires: %w", err)
	}

	comments := make([]domain.Comment, 0, len(resp.Elements))
	for _, raw := range resp.Elements {
		comments = append(comments, raw.toComment())
	}
	return comments, nil
}

// CreateComment posts a comment, or a reply when parentCommentURN is set.
func (c *Client) CreateComment(ctx context.Context, token, objectURN, actorURN, message, parentCommentURN string) (domain.Comment, error) {
	body := map[string]any{
		"actor":   actorURN,
		"object":  objectURN,
		"message": map[string]any{"text": message},
	}
	target := objectURN
	if parentCommentURN != "" {
		body["parentComment"] = parentCommentURN
		// A reply is created against the parent comment, not the post.
		target = parentCommentURN
	}

	var raw rawComment
	if err := c.do(ctx, request{
		method: http.MethodPost,
		path:   "/rest/socialActions/" + encodeURN(target) + "/comments",
		token:  token,
		body:   body,
		out:    &raw,
	}); err != nil {
		return domain.Comment{}, fmt.Errorf("publication du commentaire: %w", err)
	}
	return raw.toComment(), nil
}

// DeleteComment removes a comment from a post.
func (c *Client) DeleteComment(ctx context.Context, token, objectURN, commentID, actorURN string) error {
	if err := c.do(ctx, request{
		method: http.MethodDelete,
		path:   "/rest/socialActions/" + encodeURN(objectURN) + "/comments/" + commentID,
		token:  token,
		query:  url.Values{"actor": {actorURN}},
	}); err != nil {
		return fmt.Errorf("suppression du commentaire: %w", err)
	}
	return nil
}

// Like reacts to a post or a comment.
func (c *Client) Like(ctx context.Context, token, objectURN, actorURN string) error {
	body := map[string]any{"actor": actorURN, "object": objectURN}
	if err := c.do(ctx, request{
		method: http.MethodPost,
		path:   "/rest/socialActions/" + encodeURN(objectURN) + "/likes",
		token:  token,
		body:   body,
	}); err != nil {
		return fmt.Errorf("ajout de la réaction: %w", err)
	}
	return nil
}

// Unlike removes the member's reaction.
func (c *Client) Unlike(ctx context.Context, token, objectURN, actorURN string) error {
	if err := c.do(ctx, request{
		method: http.MethodDelete,
		path:   "/rest/socialActions/" + encodeURN(objectURN) + "/likes/" + encodeURN(actorURN),
		token:  token,
		query:  url.Values{"actor": {actorURN}},
	}); err != nil {
		return fmt.Errorf("retrait de la réaction: %w", err)
	}
	return nil
}

// ----- analytics -----

// analyticsUnsupported is the status LinkedIn answers when a metric does not
// exist for the requested shape. Like Meta, it condemns the whole batch, so
// metrics are asked for one at a time and the failures reported.
const analyticsUnsupported = http.StatusBadRequest

// PostAnalytics reads member post statistics, per post or aggregated.
//
// It needs r_member_postAnalytics, which comes from the Community Management
// access form. Without it LinkedIn answers 403 and the tool says so.
func (c *Client) PostAnalytics(ctx context.Context, token string, q domain.AnalyticsQuery) (domain.InsightSet, error) {
	set := domain.InsightSet{Insights: []domain.Insight{}}

	for _, metric := range q.Metrics {
		insight, err := c.oneMetric(ctx, token, q, metric)
		if err == nil {
			set.Insights = append(set.Insights, insight)
			continue
		}
		// Only an unsupported combination is skipped. A missing permission
		// or an expired token must reach the caller.
		if ae, ok := domain.AsAPIError(err); ok && ae.HTTPStatus == analyticsUnsupported {
			set.Rejected = append(set.Rejected, metric)
			continue
		}
		return domain.InsightSet{}, err
	}
	return set, nil
}

func (c *Client) oneMetric(ctx context.Context, token string, q domain.AnalyticsQuery, metric string) (domain.Insight, error) {
	query := url.Values{
		"queryType":   {metric},
		"aggregation": {orDefault(q.Aggregation, domain.AggregationTotal)},
	}
	if q.IsAggregate() {
		query.Set("q", "me")
	} else {
		query.Set("q", "entity")
		query.Set("entity", entityParam(q.PostURN))
	}
	if !q.Since.IsZero() {
		query.Set("dateRange", dateRange(q.Since, q.Until))
	}

	var resp struct {
		Elements []struct {
			Count      int64           `json:"count"`
			MetricType json.RawMessage `json:"metricType"`
			DateRange  struct {
				Start linkedInDate `json:"start"`
				End   linkedInDate `json:"end"`
			} `json:"dateRange"`
		} `json:"elements"`
	}
	if err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/rest/memberCreatorPostAnalytics",
		token:  token,
		query:  query,
		out:    &resp,
	}); err != nil {
		return domain.Insight{}, fmt.Errorf("statistiques: %w", err)
	}

	insight := domain.Insight{Metric: metric, Values: []domain.InsightValue{}}
	for _, e := range resp.Elements {
		count, err := json.Marshal(e.Count)
		if err != nil {
			return domain.Insight{}, fmt.Errorf("sérialisation de la valeur: %w", err)
		}
		insight.Values = append(insight.Values, domain.InsightValue{
			Start: e.DateRange.Start.String(),
			End:   e.DateRange.End.String(),
			Value: count,
		})
	}
	return insight, nil
}

// linkedInDate is the year/month/day object the analytics API uses.
type linkedInDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d linkedInDate) String() string {
	if d.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// entityParam wraps a post URN the way the analytics finder expects, which
// differs by URN type: (ugc:...) for a ugcPost, (share:...) for a share.
func entityParam(postURN string) string {
	kind := "share"
	if strings.Contains(postURN, ":ugcPost:") {
		kind = "ugc"
	}
	return "(" + kind + ":" + encodeURN(postURN) + ")"
}

// dateRange renders the Rest.li date range literal the analytics API takes.
func dateRange(since, until time.Time) string {
	var b strings.Builder
	b.WriteString("(start:")
	b.WriteString(restliDate(since))
	if !until.IsZero() {
		b.WriteString(",end:")
		b.WriteString(restliDate(until))
	}
	b.WriteString(")")
	return b.String()
}

func restliDate(t time.Time) string {
	return fmt.Sprintf("(day:%d,month:%d,year:%d)", t.Day(), int(t.Month()), t.Year())
}
