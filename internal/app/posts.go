package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edouard-claude/linkedin-mcp/internal/config"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// Bounds of the post listing.
const (
	defaultPostLimit = 25
	maxPostLimit     = 100
)

// PublishInput are the parameters of publish_post.
type PublishInput struct {
	Commentary         string
	Visibility         string
	ArticleURL         string
	ArticleTitle       string
	ArticleDescription string
	ReshareOf          string
	DisableReshare     bool
	Confirm            bool
}

// PublishOutput is both the preview and the result.
type PublishOutput struct {
	Preview    bool   `json:"preview"`
	PostURN    string `json:"post_urn,omitempty"`
	Permalink  string `json:"permalink,omitempty"`
	Author     string `json:"author"`
	Commentary string `json:"commentary,omitempty"`
	Visibility string `json:"visibility"`
	Kind       string `json:"kind"`
	ArticleURL string `json:"article_url,omitempty"`
	ReshareOf  string `json:"reshare_of,omitempty"`
	Notice     string `json:"notice,omitempty"`
}

// PublishPost creates a post, or returns a preview when unconfirmed.
func (s *Service) PublishPost(ctx context.Context, tenantID string, in PublishInput) (*PublishOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWritePosts,
		"reconnectez-vous pour accorder la publication"); err != nil {
		return nil, err
	}

	req, err := buildPublishRequest(in)
	if err != nil {
		return nil, err
	}

	out := &PublishOutput{
		Author:     tenant.DisplayName,
		Commentary: req.Commentary,
		Visibility: req.Visibility,
		Kind:       "text",
		ArticleURL: req.ArticleURL,
		ReshareOf:  req.ReshareOf,
	}
	switch {
	case req.IsReshare():
		out.Kind = "reshare"
	case req.IsArticle():
		out.Kind = "article"
	}

	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice
		return out, nil
	}

	urn, err := s.api.CreatePost(ctx, tenant.AccessToken, tenant.MemberURN(), req)
	if err != nil {
		return nil, err
	}
	out.PostURN = urn
	out.Permalink = permalink(urn)

	// The ledger is what makes list_posts work without the restricted read
	// permission, so a failure to record it is worth surfacing rather than
	// leaving a published post the server does not know about.
	now := s.clock.Now()
	if err := s.store.RecordPost(ctx, tenantID, domain.LedgerPost{
		PostURN:    urn,
		Commentary: req.Commentary,
		Visibility: req.Visibility,
		Permalink:  out.Permalink,
		CreatedAt:  now,
	}); err != nil {
		return out, fmt.Errorf("publication réussie (%s) mais son enregistrement local a échoué: %w", urn, err)
	}
	return out, nil
}

func buildPublishRequest(in PublishInput) (domain.PublishRequest, error) {
	req := domain.PublishRequest{
		Commentary:             strings.TrimSpace(in.Commentary),
		Visibility:             strings.ToUpper(strings.TrimSpace(in.Visibility)),
		ArticleURL:             strings.TrimSpace(in.ArticleURL),
		ArticleTitle:           strings.TrimSpace(in.ArticleTitle),
		ArticleDescription:     strings.TrimSpace(in.ArticleDescription),
		ReshareOf:              strings.TrimSpace(in.ReshareOf),
		DisableReshareByOthers: in.DisableReshare,
	}
	if req.Visibility == "" {
		req.Visibility = domain.VisibilityPublic
	}
	if req.Visibility != domain.VisibilityPublic && req.Visibility != domain.VisibilityConnections {
		return req, fmt.Errorf("visibility doit valoir %s ou %s",
			domain.VisibilityPublic, domain.VisibilityConnections)
	}
	if req.Commentary == "" && !req.IsReshare() {
		return req, errors.New("commentary est obligatoire, sauf pour un simple repartage")
	}
	if req.IsArticle() {
		if err := validateHTTPSURL(req.ArticleURL, "article_url"); err != nil {
			return req, err
		}
		if req.IsReshare() {
			return req, errors.New("article_url et reshare_of s'excluent")
		}
	}
	if req.IsReshare() && !strings.HasPrefix(req.ReshareOf, "urn:li:") {
		return req, errors.New("reshare_of doit être une URN LinkedIn")
	}
	return req, nil
}

func validateHTTPSURL(raw, field string) error {
	if !strings.HasPrefix(raw, "https://") || len(raw) < len("https://x.y") {
		return fmt.Errorf("%s doit être une URL https", field)
	}
	return nil
}

func permalink(urn string) string {
	if urn == "" {
		return ""
	}
	return "https://www.linkedin.com/feed/update/" + urn + "/"
}

// EditInput are the parameters of edit_post.
type EditInput struct {
	PostURN    string
	Commentary string
	Confirm    bool
}

// EditOutput is both the preview and the result.
type EditOutput struct {
	Preview       bool   `json:"preview"`
	Edited        bool   `json:"edited,omitempty"`
	PostURN       string `json:"post_urn"`
	Permalink     string `json:"permalink,omitempty"`
	OldCommentary string `json:"old_commentary,omitempty"`
	NewCommentary string `json:"new_commentary"`
	Notice        string `json:"notice,omitempty"`
}

// EditPost rewrites the text of a post. LinkedIn only lets the commentary
// change: media and links stay as published.
func (s *Service) EditPost(ctx context.Context, tenantID string, in EditInput) (*EditOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWritePosts, ""); err != nil {
		return nil, err
	}
	commentary := strings.TrimSpace(in.Commentary)
	if commentary == "" {
		return nil, errors.New("commentary est obligatoire")
	}
	post, err := s.ownedPost(ctx, tenantID, in.PostURN)
	if err != nil {
		return nil, err
	}
	if post.Deleted() {
		return nil, errors.New("cette publication a été supprimée")
	}

	out := &EditOutput{
		PostURN:       post.PostURN,
		Permalink:     post.Permalink,
		OldCommentary: post.Commentary,
		NewCommentary: commentary,
	}
	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice + " La modification est visible publiquement et LinkedIn marque la publication comme modifiée."
		return out, nil
	}

	if err := s.api.UpdatePostCommentary(ctx, tenant.AccessToken, post.PostURN, commentary); err != nil {
		return nil, err
	}
	if err := s.store.UpdateLedgerCommentary(ctx, tenantID, post.PostURN, commentary, s.clock.Now()); err != nil {
		return out, fmt.Errorf("modification réussie mais registre non mis à jour: %w", err)
	}
	out.Edited = true
	return out, nil
}

// DeleteInput are the parameters of delete_post.
type DeleteInput struct {
	PostURN string
	Confirm bool
}

// DeleteOutput is both the preview and the result.
type DeleteOutput struct {
	Preview    bool   `json:"preview"`
	Deleted    bool   `json:"deleted,omitempty"`
	PostURN    string `json:"post_urn"`
	Commentary string `json:"commentary,omitempty"`
	Notice     string `json:"notice,omitempty"`
}

// DeletePost removes a post permanently.
func (s *Service) DeletePost(ctx context.Context, tenantID string, in DeleteInput) (*DeleteOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWritePosts, ""); err != nil {
		return nil, err
	}
	post, err := s.ownedPost(ctx, tenantID, in.PostURN)
	if err != nil {
		return nil, err
	}

	out := &DeleteOutput{PostURN: post.PostURN, Commentary: post.Commentary}
	if post.Deleted() {
		out.Deleted = true
		return out, nil
	}
	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice +
			" La suppression est définitive : la publication, ses réactions et ses commentaires disparaissent."
		return out, nil
	}

	if err := s.api.DeletePost(ctx, tenant.AccessToken, post.PostURN); err != nil {
		return nil, err
	}
	if err := s.store.MarkPostDeleted(ctx, tenantID, post.PostURN, s.clock.Now()); err != nil {
		return out, fmt.Errorf("suppression réussie mais registre non mis à jour: %w", err)
	}
	out.Deleted = true
	return out, nil
}

// ListPostsInput are the parameters of list_posts.
type ListPostsInput struct {
	Limit          int
	IncludeDeleted bool
}

// ListPostsOutput carries the posts and says where they came from.
type ListPostsOutput struct {
	Posts []domain.Post `json:"posts"`
	// Source is "linkedin" when the restricted read permission let us ask
	// LinkedIn directly, "ledger" when the list is what this server
	// published itself.
	Source string `json:"source"`
	Notice string `json:"notice,omitempty"`
}

// ListPosts returns the member's posts.
//
// Listing a member's own posts needs r_member_social, which LinkedIn grants
// to approved apps only. When it is absent the answer comes from the local
// ledger, which holds everything published through this server, and the
// output says so rather than pretending to be exhaustive.
func (s *Service) ListPosts(ctx context.Context, tenantID string, in ListPostsInput) (*ListPostsOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	limit := clampLimit(in.Limit, defaultPostLimit, maxPostLimit)

	if tenant.HasScope(config.ScopeReadPosts) {
		posts, err := s.api.FindPostsByAuthor(ctx, tenant.AccessToken, tenant.MemberURN(), limit)
		if err == nil {
			return &ListPostsOutput{Posts: posts, Source: domain.SourceLinkedIn}, nil
		}
		// Fall through to the ledger rather than failing: a degraded list is
		// more useful than none.
		if ae, ok := domain.AsAPIError(err); !ok || !ae.IsAuth() {
			return nil, err
		}
	}

	ledger, err := s.store.LedgerPosts(ctx, tenantID, in.IncludeDeleted, limit)
	if err != nil {
		return nil, fmt.Errorf("lecture du registre: %w", err)
	}
	posts := make([]domain.Post, 0, len(ledger))
	for _, p := range ledger {
		post := domain.Post{
			URN:        p.PostURN,
			Commentary: p.Commentary,
			Visibility: p.Visibility,
			Permalink:  p.Permalink,
			CreatedAt:  p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Source:     domain.SourceLedger,
		}
		if p.Deleted() {
			post.LifecycleState = "DELETED"
		}
		posts = append(posts, post)
	}
	return &ListPostsOutput{
		Posts:  posts,
		Source: domain.SourceLedger,
		Notice: "Liste issue du registre local : elle contient les publications faites depuis ce serveur, " +
			"pas celles écrites directement sur LinkedIn. La permission r_member_social, restreinte, lèverait cette limite.",
	}, nil
}
