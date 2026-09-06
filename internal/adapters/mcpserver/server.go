// Package mcpserver exposes the use cases as MCP tools over Streamable HTTP.
//
// Every tool reads its tenant from the verified bearer token, never from a
// parameter, and every post URN a client sends is checked against that
// tenant's ledger before anything reaches LinkedIn.
package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/edouard-claude/linkedin-mcp/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "linkedin-mcp"
	serverVersion = "v1.0.0"
	serverTitle   = "LinkedIn personnel"
)

type deps struct {
	svc    *app.Service
	logger *slog.Logger
}

// New builds the MCP server with every feature registered.
func New(svc *app.Service, logger *slog.Logger) *mcp.Server {
	d := &deps{svc: svc, logger: logger}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
		Title:   serverTitle,
	}, &mcp.ServerOptions{Instructions: instructions})

	d.registerResources(srv)
	d.registerPrompts(srv)
	d.registerReadTools(srv)
	d.registerWriteTools(srv)
	return srv
}

const instructions = `Serveur MCP pour le compte LinkedIn personnel de l'utilisateur connecté.

Commencez par connection_status : il dit ce que le compte a le droit de faire.
LinkedIn découpe ses permissions finement et toutes ne sont pas accordées par
défaut, donc un outil peut être indisponible sans que rien soit cassé.

Lecture : list_posts (publications), post_engagement (réactions et
commentaires d'une publication), post_comments (le détail des commentaires),
post_analytics et account_analytics (statistiques détaillées, si la permission
r_member_postAnalytics a été accordée).

Écriture : publish_post, edit_post, delete_post, publish_comment,
delete_comment, react. Toutes exigent confirm=true. Sans ce paramètre elles
renvoient un aperçu et n'écrivent rien : montrez l'aperçu à l'utilisateur,
obtenez son accord explicite, puis rappelez l'outil avec confirm=true.

Deux limites à connaître et à expliquer plutôt qu'à contourner. list_posts ne
voit que les publications faites depuis ce serveur tant que la permission
r_member_social, restreinte, n'est pas accordée. Et LinkedIn ne renouvelle pas
les jetons tout seul : au bout de 60 jours l'utilisateur doit repasser par
reconnect_url.`

// tenantID extracts the tenant from the verified bearer token.
func tenantID(req *mcp.CallToolRequest) (string, error) {
	if req == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return "", errSessionUnauthenticated
	}
	return req.Extra.TokenInfo.UserID, nil
}

// errSessionUnauthenticated means a request reached a handler without a
// verified tenant, which can only be a wiring bug.
var errSessionUnauthenticated = errors.New("session non authentifiée")

// errPostURNRequired is what post_analytics answers without a post: the
// account-wide variant is a separate tool on purpose.
var errPostURNRequired = errors.New("post_urn est obligatoire, utilisez account_analytics pour l'ensemble du compte")

// jsonResult packs a value as the single compact JSON text block every tool
// returns.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("sérialisation du résultat: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
	}, nil, nil
}

// toolError turns an internal error into the sentence the user reads.
func (d *deps) toolError(tool string, err error) error {
	var scopeErr *domain.ErrScopeMissing
	if errors.As(err, &scopeErr) {
		return err
	}
	if ae, ok := domain.AsAPIError(err); ok {
		d.logger.Warn("erreur LinkedIn", "tool", tool, "status", ae.HTTPStatus, "code", ae.Code)
		return errors.New(ae.UserMessage())
	}
	if errors.Is(err, domain.ErrNotFound) {
		return errors.New("introuvable pour ce compte")
	}
	d.logger.Error("erreur d'outil", "tool", tool, "error", err)
	return err
}

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: ptr(false),
		OpenWorldHint:   ptr(true),
	}
}

func writing() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  false,
		OpenWorldHint: ptr(true),
	}
}

// destructive annotates a tool that removes something for good.
func destructive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: ptr(true),
		OpenWorldHint:   ptr(true),
	}
}

func ptr[T any](v T) *T { return &v }
