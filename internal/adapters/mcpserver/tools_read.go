package mcpserver

import (
	"context"

	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NoArgs is the input of the tools that take no parameter.
type NoArgs struct{}

func (d *deps) registerReadTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "connection_status",
		Title:       "État de la connexion",
		Description: "Vérifie auprès de LinkedIn que l'autorisation tient toujours, et dit ce que le compte a le droit de faire : publier, commenter, lire ses publications, accéder aux statistiques. À appeler en premier, et dès qu'un outil échoue.",
		Annotations: readOnly(),
	}, d.toolConnectionStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_posts",
		Title:       "Lister les publications",
		Description: "Publications du compte, les plus récentes d'abord. Tant que la permission restreinte r_member_social n'est pas accordée, la liste ne contient que les publications faites depuis ce serveur : le champ source le dit.",
		Annotations: readOnly(),
	}, d.toolListPosts)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "post_engagement",
		Title:       "Engagement d'une publication",
		Description: "Nombre de réactions et de commentaires d'une publication, et si l'utilisateur l'a lui-même likée. Ne demande aucune permission restreinte : c'est la mesure disponible par défaut.",
		Annotations: readOnly(),
	}, d.toolPostEngagement)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "post_comments",
		Title:       "Commentaires d'une publication",
		Description: "Commentaires laissés sur une publication. Passez l'URN d'un commentaire au lieu de celle d'une publication pour obtenir ses réponses.",
		Annotations: readOnly(),
	}, d.toolPostComments)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "post_analytics",
		Title:       "Statistiques d'une publication",
		Description: "Statistiques détaillées d'une publication : impressions, comptes atteints, réactions, commentaires, repartages, enregistrements, clics, abonnés gagnés et vues de profil générées. Demande la permission r_member_postAnalytics, obtenue via le formulaire Community Management de LinkedIn.",
		Annotations: readOnly(),
	}, d.toolPostAnalytics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "account_analytics",
		Title:       "Statistiques du compte",
		Description: "Mêmes métriques que post_analytics mais agrégées sur toutes les publications du compte. Utile pour suivre une tendance plutôt qu'une publication.",
		Annotations: readOnly(),
	}, d.toolAccountAnalytics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reconnect_url",
		Title:       "Lien de reconnexion",
		Description: "Adresse à ouvrir pour réautoriser LinkedIn, quand un outil répond « autorisation expirée » ou avant l'échéance des 60 jours.",
		Annotations: readOnly(),
	}, d.toolReconnectURL)
}

func (d *deps) toolConnectionStatus(ctx context.Context, req *mcp.CallToolRequest, _ NoArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	status, err := d.svc.ConnectionStatus(ctx, tenant)
	if err != nil {
		return nil, nil, d.toolError("connection_status", err)
	}
	return jsonResult(status)
}

// ListPostsArgs are the arguments of list_posts.
type ListPostsArgs struct {
	Limit          int  `json:"limit,omitempty" jsonschema:"Nombre maximum de publications, 25 par défaut, 100 au maximum."`
	IncludeDeleted bool `json:"include_deleted,omitempty" jsonschema:"Inclure les publications supprimées depuis ce serveur, marquées lifecycle_state=DELETED."`
}

func (d *deps) toolListPosts(ctx context.Context, req *mcp.CallToolRequest, args ListPostsArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.ListPosts(ctx, tenant, app.ListPostsInput{
		Limit:          args.Limit,
		IncludeDeleted: args.IncludeDeleted,
	})
	if err != nil {
		return nil, nil, d.toolError("list_posts", err)
	}
	return jsonResult(out)
}

// PostArgs is the input of the tools that only need a post.
type PostArgs struct {
	PostURN string `json:"post_urn" jsonschema:"URN de la publication, par exemple urn:li:share:7325786486870552578, telle que renvoyée par list_posts ou publish_post."`
}

func (d *deps) toolPostEngagement(ctx context.Context, req *mcp.CallToolRequest, args PostArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	out, err := d.svc.PostEngagement(ctx, tenant, args.PostURN)
	if err != nil {
		return nil, nil, d.toolError("post_engagement", err)
	}
	return jsonResult(out)
}

// CommentsArgs are the arguments of post_comments.
type CommentsArgs struct {
	ObjectURN string `json:"object_urn" jsonschema:"URN de la publication, ou d'un commentaire pour obtenir ses réponses."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Nombre maximum de commentaires, 25 par défaut, 100 au maximum."`
}

func (d *deps) toolPostComments(ctx context.Context, req *mcp.CallToolRequest, args CommentsArgs) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	comments, err := d.svc.PostComments(ctx, tenant, app.CommentsInput{
		ObjectURN: args.ObjectURN,
		Limit:     args.Limit,
	})
	if err != nil {
		return nil, nil, d.toolError("post_comments", err)
	}
	return jsonResult(comments)
}

// AnalyticsArgs are the arguments of post_analytics and account_analytics.
type AnalyticsArgs struct {
	PostURN string   `json:"post_urn,omitempty" jsonschema:"URN de la publication. Absent sur account_analytics, qui agrège tout le compte."`
	Metrics []string `json:"metrics,omitempty" jsonschema:"Métriques à lire. Par défaut IMPRESSION, MEMBERS_REACHED, REACTION, COMMENT, RESHARE. Autres valeurs: POST_SAVE, POST_SEND, LINK_CLICKS, PREMIUM_CTA_CLICKS, FOLLOWER_GAINED_FROM_CONTENT, PROFILE_VIEW_FROM_CONTENT."`
	Daily   bool     `json:"daily,omitempty" jsonschema:"Ventiler par jour au lieu d'un total. Certaines métriques n'existent qu'en total et sont alors retirées, le champ notice le dit."`
	Since   string   `json:"since,omitempty" jsonschema:"Début de la période, au format AAAA-MM-JJ. Absent = durée de vie de la publication."`
	Until   string   `json:"until,omitempty" jsonschema:"Fin de la période, au format AAAA-MM-JJ."`
}

func (d *deps) toolPostAnalytics(ctx context.Context, req *mcp.CallToolRequest, args AnalyticsArgs) (*mcp.CallToolResult, any, error) {
	return d.analytics(ctx, req, args, "post_analytics", true)
}

func (d *deps) toolAccountAnalytics(ctx context.Context, req *mcp.CallToolRequest, args AnalyticsArgs) (*mcp.CallToolResult, any, error) {
	args.PostURN = ""
	return d.analytics(ctx, req, args, "account_analytics", false)
}

func (d *deps) analytics(ctx context.Context, req *mcp.CallToolRequest, args AnalyticsArgs, tool string, needsPost bool) (*mcp.CallToolResult, any, error) {
	tenant, err := tenantID(req)
	if err != nil {
		return nil, nil, err
	}
	if needsPost && args.PostURN == "" {
		return nil, nil, errPostURNRequired
	}
	out, err := d.svc.Analytics(ctx, tenant, app.AnalyticsInput{
		PostURN: args.PostURN,
		Metrics: args.Metrics,
		Daily:   args.Daily,
		Since:   args.Since,
		Until:   args.Until,
	})
	if err != nil {
		return nil, nil, d.toolError(tool, err)
	}
	return jsonResult(out)
}

func (d *deps) toolReconnectURL(ctx context.Context, req *mcp.CallToolRequest, _ NoArgs) (*mcp.CallToolResult, any, error) {
	if _, err := tenantID(req); err != nil {
		return nil, nil, err
	}
	url, err := d.svc.ReconnectURL(ctx)
	if err != nil {
		return nil, nil, d.toolError("reconnect_url", err)
	}
	return jsonResult(map[string]string{"url": url})
}
