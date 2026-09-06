package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edouard-claude/linkedin-mcp/internal/config"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// Bounds of the comment listing.
const (
	defaultCommentLimit = 25
	maxCommentLimit     = 100
)

// PostEngagement is what post_engagement returns: the counters LinkedIn
// exposes without the restricted analytics permission.
type PostEngagement struct {
	PostURN   string `json:"post_urn"`
	Permalink string `json:"permalink,omitempty"`
	Likes     int64  `json:"likes"`
	Comments  int64  `json:"comments"`
	LikedByMe bool   `json:"liked_by_me"`
	// CommentsState is OPEN or CLOSED.
	CommentsState string `json:"comments_state,omitempty"`
	Notice        string `json:"notice,omitempty"`
}

// PostEngagement reads the like and comment counters of a post.
//
// This is the poor man's analytics: it needs no restricted permission, and it
// already answers "how is my post doing" for most people.
func (s *Service) PostEngagement(ctx context.Context, tenantID, postURN string) (*PostEngagement, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(postURN) == "" {
		return nil, errors.New("post_urn est obligatoire")
	}

	counts, err := s.api.SocialCounts(ctx, tenant.AccessToken, postURN)
	if err != nil {
		return nil, err
	}
	out := &PostEngagement{
		PostURN:       postURN,
		Permalink:     permalink(postURN),
		Likes:         counts.Likes,
		Comments:      counts.Comments,
		LikedByMe:     counts.LikedByMe,
		CommentsState: counts.CommentsState,
	}
	if counts.Likes == 0 && counts.Comments == 0 {
		out.Notice = "Aucune réaction ni commentaire pour l'instant. LinkedIn ne distingue pas ce cas d'une publication qu'il ne trouve pas."
	}
	return out, nil
}

// CommentsInput are the parameters of post_comments.
type CommentsInput struct {
	ObjectURN string
	Limit     int
}

// PostComments lists the comments on a post, or the replies to a comment.
func (s *Service) PostComments(ctx context.Context, tenantID string, in CommentsInput) ([]domain.Comment, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ObjectURN) == "" {
		return nil, errors.New("object_urn est obligatoire")
	}
	return s.api.Comments(ctx, tenant.AccessToken, in.ObjectURN,
		clampLimit(in.Limit, defaultCommentLimit, maxCommentLimit))
}

// CommentInput are the parameters of publish_comment.
type CommentInput struct {
	ObjectURN string
	Message   string
	ParentURN string
	Confirm   bool
}

// CommentOutput is both the preview and the result.
type CommentOutput struct {
	Preview    bool   `json:"preview"`
	CommentURN string `json:"comment_urn,omitempty"`
	CommentID  string `json:"comment_id,omitempty"`
	ObjectURN  string `json:"object_urn"`
	ParentURN  string `json:"parent_comment_urn,omitempty"`
	Message    string `json:"message"`
	Notice     string `json:"notice,omitempty"`
}

// PublishComment comments on a post, or replies to a comment.
func (s *Service) PublishComment(ctx context.Context, tenantID string, in CommentInput) (*CommentOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWriteFeed,
		"reconnectez-vous pour accorder les commentaires et réactions"); err != nil {
		return nil, err
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		return nil, errors.New("message est obligatoire")
	}
	if strings.TrimSpace(in.ObjectURN) == "" {
		return nil, errors.New("object_urn est obligatoire")
	}

	out := &CommentOutput{
		ObjectURN: in.ObjectURN,
		ParentURN: in.ParentURN,
		Message:   message,
	}
	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice
		return out, nil
	}

	comment, err := s.api.CreateComment(ctx, tenant.AccessToken,
		in.ObjectURN, tenant.MemberURN(), message, in.ParentURN)
	if err != nil {
		return nil, err
	}
	out.CommentURN = comment.CommentURN
	out.CommentID = comment.CommentID
	return out, nil
}

// DeleteCommentInput are the parameters of delete_comment.
type DeleteCommentInput struct {
	ObjectURN string
	CommentID string
	Confirm   bool
}

// DeleteCommentOutput is both the preview and the result.
type DeleteCommentOutput struct {
	Preview   bool   `json:"preview"`
	Deleted   bool   `json:"deleted,omitempty"`
	ObjectURN string `json:"object_urn"`
	CommentID string `json:"comment_id"`
	Notice    string `json:"notice,omitempty"`
}

// DeleteComment removes one of the member's own comments.
func (s *Service) DeleteComment(ctx context.Context, tenantID string, in DeleteCommentInput) (*DeleteCommentOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWriteFeed, ""); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ObjectURN) == "" || strings.TrimSpace(in.CommentID) == "" {
		return nil, errors.New("object_urn et comment_id sont obligatoires")
	}

	out := &DeleteCommentOutput{ObjectURN: in.ObjectURN, CommentID: in.CommentID}
	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice + " La suppression est définitive."
		return out, nil
	}

	if err := s.api.DeleteComment(ctx, tenant.AccessToken,
		in.ObjectURN, in.CommentID, tenant.MemberURN()); err != nil {
		return nil, err
	}
	out.Deleted = true
	return out, nil
}

// ReactInput are the parameters of react.
type ReactInput struct {
	ObjectURN string
	Remove    bool
	Confirm   bool
}

// ReactOutput is both the preview and the result.
type ReactOutput struct {
	Preview   bool   `json:"preview"`
	Done      bool   `json:"done,omitempty"`
	ObjectURN string `json:"object_urn"`
	Action    string `json:"action"`
	Notice    string `json:"notice,omitempty"`
}

// React likes or unlikes a post or a comment.
func (s *Service) React(ctx context.Context, tenantID string, in ReactInput) (*ReactOutput, error) {
	tenant, err := s.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := requireScope(tenant, config.ScopeWriteFeed, ""); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ObjectURN) == "" {
		return nil, errors.New("object_urn est obligatoire")
	}

	action := "like"
	if in.Remove {
		action = "unlike"
	}
	out := &ReactOutput{ObjectURN: in.ObjectURN, Action: action}
	if !in.Confirm {
		out.Preview = true
		out.Notice = previewNotice
		return out, nil
	}

	if in.Remove {
		err = s.api.Unlike(ctx, tenant.AccessToken, in.ObjectURN, tenant.MemberURN())
	} else {
		err = s.api.Like(ctx, tenant.AccessToken, in.ObjectURN, tenant.MemberURN())
	}
	if err != nil {
		return nil, fmt.Errorf("réaction: %w", err)
	}
	out.Done = true
	return out, nil
}
