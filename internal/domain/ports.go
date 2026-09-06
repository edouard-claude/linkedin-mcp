package domain

import (
	"context"
	"errors"
	"time"
)

// Clock abstracts time so that expiry logic is testable.
type Clock interface {
	Now() time.Time
}

// TokenCipher seals and opens the LinkedIn tokens stored in the database.
type TokenCipher interface {
	Encrypt(plaintext string) ([]byte, error)
	Decrypt(ciphertext []byte) (string, error)
}

// TenantStore persists tenants, the post ledger and the whole OAuth state.
//
// Every content-scoped method takes a tenantID: there is deliberately no
// method that resolves a post on its own.
type TenantStore interface {
	UpsertTenant(ctx context.Context, t *Tenant) error
	TenantByID(ctx context.Context, id string) (*Tenant, error)
	TenantByMemberID(ctx context.Context, memberID string) (*Tenant, error)
	DeleteTenant(ctx context.Context, id string) error
	TenantsDueForTokenRefresh(ctx context.Context, expiringBefore, uncheckedBefore time.Time) ([]Tenant, error)

	// RecordPost appends to the ledger of posts this server published. It is
	// what makes a listing possible without the restricted read permission.
	RecordPost(ctx context.Context, tenantID string, p LedgerPost) error
	MarkPostDeleted(ctx context.Context, tenantID, postURN string, at time.Time) error
	UpdateLedgerCommentary(ctx context.Context, tenantID, postURN, commentary string, at time.Time) error
	LedgerPosts(ctx context.Context, tenantID string, includeDeleted bool, limit int) ([]LedgerPost, error)
	LedgerPost(ctx context.Context, tenantID, postURN string) (*LedgerPost, error)

	RegisterClient(ctx context.Context, c *OAuthClient) error
	ClientByID(ctx context.Context, clientID string) (*OAuthClient, error)

	CreateAuthCode(ctx context.Context, c *AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (*AuthCode, error)

	CreateRefreshToken(ctx context.Context, rt *RefreshToken) error
	RotateRefreshToken(ctx context.Context, tokenHash string, now time.Time) (*RefreshToken, error)
	RevokeTenantRefreshTokens(ctx context.Context, tenantID string) error

	CreateLoginState(ctx context.Context, s *LoginState) error
	ConsumeLoginState(ctx context.Context, state string) (*LoginState, error)

	PurgeExpired(ctx context.Context, now time.Time) error

	Ping(ctx context.Context) error
	Close() error
}

// LedgerPost is a post this server published, recorded locally.
type LedgerPost struct {
	PostURN    string    `json:"post_urn"`
	Commentary string    `json:"commentary,omitempty"`
	Visibility string    `json:"visibility,omitempty"`
	Permalink  string    `json:"permalink,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
	DeletedAt  time.Time `json:"deleted_at,omitzero"`
}

// Deleted reports whether the post was removed through this server.
func (p *LedgerPost) Deleted() bool { return !p.DeletedAt.IsZero() }

// LinkedInOAuth is the slice of LinkedIn used by the login flow. It is a port
// of its own so the login use case never has to know about posts.
type LinkedInOAuth interface {
	AuthorizeURL(redirectURI, state string) string
	ExchangeCode(ctx context.Context, code, redirectURI string) (Token, error)
	// RefreshAccessToken works only for apps LinkedIn granted programmatic
	// refresh tokens to; otherwise it returns ErrRefreshUnavailable.
	RefreshAccessToken(ctx context.Context, refreshToken string) (Token, error)
	Me(ctx context.Context, accessToken string) (Member, error)
	// IntrospectToken reports what LinkedIn thinks of a token.
	IntrospectToken(ctx context.Context, accessToken string) (TokenStatus, error)
}

// ErrRefreshUnavailable means LinkedIn issued no refresh token for this app,
// which is the default: only approved partners get them.
var ErrRefreshUnavailable = errors.New("LinkedIn n'a pas délivré de refresh token à cette application")

// TokenStatus is what LinkedIn's token introspection says.
type TokenStatus struct {
	Active    bool
	ExpiresAt time.Time
	Scopes    []string
	Status    string
	Reason    string
}

// Client is everything the application needs from LinkedIn.
type Client interface {
	LinkedInOAuth

	// --- posts ---

	CreatePost(ctx context.Context, token, authorURN string, req PublishRequest) (string, error)
	UpdatePostCommentary(ctx context.Context, token, postURN, commentary string) error
	DeletePost(ctx context.Context, token, postURN string) error
	// GetPost needs r_member_social, which is restricted.
	GetPost(ctx context.Context, token, postURN string) (Post, error)
	// FindPostsByAuthor needs r_member_social, which is restricted.
	FindPostsByAuthor(ctx context.Context, token, authorURN string, limit int) ([]Post, error)

	// --- social actions ---

	SocialCounts(ctx context.Context, token, objectURN string) (SocialCounts, error)
	Comments(ctx context.Context, token, objectURN string, limit int) ([]Comment, error)
	CreateComment(ctx context.Context, token, objectURN, actorURN, message, parentCommentURN string) (Comment, error)
	DeleteComment(ctx context.Context, token, objectURN, commentID, actorURN string) error
	Like(ctx context.Context, token, objectURN, actorURN string) error
	Unlike(ctx context.Context, token, objectURN, actorURN string) error

	// --- analytics ---

	// PostAnalytics needs r_member_postAnalytics, granted through the
	// Community Management access form.
	PostAnalytics(ctx context.Context, token string, q AnalyticsQuery) (InsightSet, error)
}
