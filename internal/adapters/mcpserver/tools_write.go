package mcpserver

import (
	"context"

	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// confirmDoc is appended to every write tool description so the calling model
// cannot miss the rule.
const confirmDoc = " ÉCRITURE : sans confirm=true, l'outil renvoie seulement un aperçu et " +
	"n'envoie rien à LinkedIn. Montrez l'aperçu à l'utilisateur, obtenez son accord explicite, " +
	"puis rappelez l'outil avec confirm=true."

func (d *deps) registerWriteTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "publish_post",
		Title:       "Publier",
		Description: "Publie sur le profil LinkedIn de l'utilisateur : texte seul, partage de lien, ou repartage d'une publication existante." + confirmDoc,
		Annotations: writing(),
	}, d.toolPublishPost)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "edit_post",
		Title:       "Modifier une publication",
		Description: "Réécrit le texte d'une publication. LinkedIn n'autorise que le texte : le lien ou le média restent ceux d'origine, et la publication est marquée comme modifiée." + confirmDoc,
		Annotations: writing(),
	}, d.toolEditPost)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "publish_comment",
		Title:       "Commenter",
		Description: "Commente une publication, ou répond à un commentaire en passant parent_comment_urn." + confirmDoc,
		Annotations: writing(),
	}, d.toolPublishComment)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "react",
		Title:       "Réagir",
		Description: "Ajoute ou retire une réaction sur une publication ou un commentaire." + confirmDoc,
		Annotations: writing(),
	}, d.toolReact)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_post",
		Title:       "Supprimer une publication",
		Description: "Supprime définitivement une publication. Ses réactions et ses commentaires disparaissent avec elle, et rien ne les restaure." + confirmDoc,
		Annotations: destructive(),
	}, d.toolDeletePost)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_comment",
		Title:       "Supprimer un commentaire",
		Description: "Supprime définitivement un commentaire de l'utilisateur." + confirmDoc,
		Annotations: destructive(),
	}, d.toolDeleteComment)
}

// PublishArgs are the arguments of publish_post.
type PublishArgs struct {
	Commentary         string `json:"commentary,omitempty" jsonschema:"Texte de la publication. Obligatoire sauf pour un simple repartage."`
	Visibility         string `json:"visibility,omitempty" jsonschema:"PUBLIC (défaut) ou CONNECTIONS pour ne toucher que ses relations."`
	ArticleURL         string `json:"article_url,omitempty" jsonschema:"URL https à partager en aperçu de lien."`
	ArticleTitle       string `json:"article_title,omitempty" jsonschema:"Titre de l'aperçu. LinkedIn ne lit pas la page, donc sans titre l'aperçu sera vide."`
	ArticleDescription string `json:"article_description,omitempty" jsonschema:"Description de l'aperçu de lien."`
	ReshareOf          string `json:"reshare_of,omitempty" jsonschema:"URN d'une publication à repartager."`
	DisableReshare     bool   `json:"disable_reshare,omitempty" jsonschema:"Empêcher les autres de repartager cette publication."`
	Confirm            bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour publier réellement. false ou absent renvoie un aperçu."`
}

func (d *deps) toolPublishPost(ctx context.Context, req *mcp.CallToolRequest, args PublishArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.PublishPost(ctx, tenant, app.PublishInput{
		Commentary:         args.Commentary,
		Visibility:         args.Visibility,
		ArticleURL:         args.ArticleURL,
		ArticleTitle:       args.ArticleTitle,
		ArticleDescription: args.ArticleDescription,
		ReshareOf:          args.ReshareOf,
		DisableReshare:     args.DisableReshare,
		Confirm:            args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("publish_post", err)
	}
	if !out.Preview {
		d.logger.Info("publication créée", "post_urn", out.PostURN)
	}
	return jsonResult(out)
}

// EditArgs are the arguments of edit_post.
type EditArgs struct {
	PostURN    string `json:"post_urn" jsonschema:"URN de la publication à modifier."`
	Commentary string `json:"commentary" jsonschema:"Nouveau texte, il remplace intégralement l'ancien."`
	Confirm    bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour modifier réellement."`
}

func (d *deps) toolEditPost(ctx context.Context, req *mcp.CallToolRequest, args EditArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.EditPost(ctx, tenant, app.EditInput{
		PostURN:    args.PostURN,
		Commentary: args.Commentary,
		Confirm:    args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("edit_post", err)
	}
	return jsonResult(out)
}

// DeletePostArgs are the arguments of delete_post.
type DeletePostArgs struct {
	PostURN string `json:"post_urn" jsonschema:"URN de la publication à supprimer."`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour supprimer réellement."`
}

func (d *deps) toolDeletePost(ctx context.Context, req *mcp.CallToolRequest, args DeletePostArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.DeletePost(ctx, tenant, app.DeleteInput{
		PostURN: args.PostURN,
		Confirm: args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("delete_post", err)
	}
	if out.Deleted {
		d.logger.Info("publication supprimée", "post_urn", out.PostURN)
	}
	return jsonResult(out)
}

// CommentArgs are the arguments of publish_comment.
type CommentArgs struct {
	ObjectURN string `json:"object_urn" jsonschema:"URN de la publication à commenter."`
	Message   string `json:"message" jsonschema:"Texte du commentaire."`
	ParentURN string `json:"parent_comment_urn,omitempty" jsonschema:"URN d'un commentaire pour y répondre au lieu de commenter la publication."`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour publier réellement."`
}

func (d *deps) toolPublishComment(ctx context.Context, req *mcp.CallToolRequest, args CommentArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.PublishComment(ctx, tenant, app.CommentInput{
		ObjectURN: args.ObjectURN,
		Message:   args.Message,
		ParentURN: args.ParentURN,
		Confirm:   args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("publish_comment", err)
	}
	return jsonResult(out)
}

// DeleteCommentArgs are the arguments of delete_comment.
type DeleteCommentArgs struct {
	ObjectURN string `json:"object_urn" jsonschema:"URN de la publication qui porte le commentaire."`
	CommentID string `json:"comment_id" jsonschema:"Identifiant du commentaire, le champ comment_id renvoyé par post_comments."`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour supprimer réellement."`
}

func (d *deps) toolDeleteComment(ctx context.Context, req *mcp.CallToolRequest, args DeleteCommentArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.DeleteComment(ctx, tenant, app.DeleteCommentInput{
		ObjectURN: args.ObjectURN,
		CommentID: args.CommentID,
		Confirm:   args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("delete_comment", err)
	}
	return jsonResult(out)
}

// ReactArgs are the arguments of react.
type ReactArgs struct {
	ObjectURN string `json:"object_urn" jsonschema:"URN de la publication ou du commentaire."`
	Remove    bool   `json:"remove,omitempty" jsonschema:"true pour retirer la réaction au lieu de l'ajouter."`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"Doit valoir true pour appliquer réellement."`
}

func (d *deps) toolReact(ctx context.Context, req *mcp.CallToolRequest, args ReactArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.React(ctx, tenant, app.ReactInput{
		ObjectURN: args.ObjectURN,
		Remove:    args.Remove,
		Confirm:   args.Confirm,
	})
	if err != nil {
		return nil, nil, d.toolError("react", err)
	}
	return jsonResult(out)
}
