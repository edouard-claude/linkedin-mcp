package domain

import (
	"encoding/json"
	"time"
)

// Post visibility values accepted by the Posts API.
const (
	VisibilityPublic      = "PUBLIC"
	VisibilityConnections = "CONNECTIONS"
)

// Post is a LinkedIn post, as this server exposes it.
type Post struct {
	URN        string `json:"post_urn"`
	Commentary string `json:"commentary,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	// LifecycleState is PUBLISHED, DRAFT, PROCESSING and so on.
	LifecycleState string `json:"lifecycle_state,omitempty"`
	Permalink      string `json:"permalink,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	LastModifiedAt string `json:"last_modified_at,omitempty"`
	EditedByAuthor bool   `json:"edited_by_author,omitempty"`
	// Source says where this row came from: "linkedin" when read from the
	// API, "ledger" when replayed from what this server published. The
	// distinction matters because listing a member's own posts needs a
	// restricted permission most apps do not have.
	Source string `json:"source,omitempty"`
}

// LedgerSource marks a post reconstructed from the local ledger.
const (
	SourceLinkedIn = "linkedin"
	SourceLedger   = "ledger"
)

// PublishRequest describes a post to create.
type PublishRequest struct {
	Commentary string
	Visibility string
	// ArticleURL, when set, attaches a link preview. LinkedIn does not
	// scrape it, so a title is worth providing.
	ArticleURL         string
	ArticleTitle       string
	ArticleDescription string
	// ReshareOf, when set, reshares that post URN.
	ReshareOf              string
	DisableReshareByOthers bool
}

// IsArticle reports whether the post carries a link preview.
func (r PublishRequest) IsArticle() bool { return r.ArticleURL != "" }

// IsReshare reports whether the post reshares another one.
func (r PublishRequest) IsReshare() bool { return r.ReshareOf != "" }

// Comment is a comment on a post or on another comment.
type Comment struct {
	CommentURN string `json:"comment_urn"`
	CommentID  string `json:"comment_id"`
	ActorURN   string `json:"actor_urn,omitempty"`
	Message    string `json:"message"`
	ObjectURN  string `json:"object_urn,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	LikeCount  int64  `json:"like_count,omitempty"`
	ReplyCount int64  `json:"reply_count,omitempty"`
	ParentURN  string `json:"parent_comment_urn,omitempty"`
}

// SocialCounts is the engagement summary of a post: what /socialActions
// returns without needing the restricted read permission on the post itself.
type SocialCounts struct {
	ObjectURN     string `json:"object_urn"`
	Likes         int64  `json:"likes"`
	Comments      int64  `json:"comments"`
	CommentsState string `json:"comments_state,omitempty"`
	LikedByMe     bool   `json:"liked_by_me"`
}

// Insight is one analytics metric, in the shape the analytics API returns.
type Insight struct {
	Metric string         `json:"metric"`
	Values []InsightValue `json:"values"`
}

// InsightValue is a single data point. Value is raw JSON because LinkedIn
// returns integers today and could return more later.
type InsightValue struct {
	Start string          `json:"start,omitempty"`
	End   string          `json:"end,omitempty"`
	Value json.RawMessage `json:"value"`
}

// InsightSet is the answer of an analytics query. Metrics LinkedIn refused
// land in Rejected rather than failing the whole call.
type InsightSet struct {
	Insights []Insight `json:"insights"`
	Rejected []string  `json:"rejected,omitempty"`
}

// Analytics aggregation modes.
const (
	AggregationTotal = "TOTAL"
	AggregationDaily = "DAILY"
)

// AnalyticsQuery is a member post analytics request.
type AnalyticsQuery struct {
	// PostURN empty means the aggregate over every post of the member.
	PostURN     string
	Metrics     []string
	Aggregation string
	Since       time.Time
	Until       time.Time
}

// IsAggregate reports whether the query covers the whole account.
func (q AnalyticsQuery) IsAggregate() bool { return q.PostURN == "" }
