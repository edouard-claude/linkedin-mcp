package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Resource URIs. They are tenant-scoped like everything else: the same URI
// gives each member their own data, resolved from their bearer token.
const (
	uriConnection = "linkedin://connection"
	uriPosts      = "linkedin://posts"
)

func (d *deps) registerResources(srv *mcp.Server) {
	srv.AddResource(&mcp.Resource{
		URI:         uriConnection,
		Name:        "connection",
		Title:       "État de la connexion",
		Description: "Validité du jeton LinkedIn, expiration, permissions accordées et ce que le compte peut faire.",
		MIMEType:    "application/json",
	}, d.readConnection)

	srv.AddResource(&mcp.Resource{
		URI:         uriPosts,
		Name:        "posts",
		Title:       "Publications",
		Description: "Publications récentes du compte, telles que list_posts les renvoie.",
		MIMEType:    "application/json",
	}, d.readPosts)
}

func (d *deps) readConnection(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	tenant, err := resourceTenantID(req)
	if err != nil {
		return nil, err
	}
	status, err := d.svc.ConnectionStatus(ctx, tenant)
	if err != nil {
		return nil, d.toolError("resource:connection", err)
	}
	return jsonResource(uriConnection, status)
}

func (d *deps) readPosts(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	tenant, err := resourceTenantID(req)
	if err != nil {
		return nil, err
	}
	posts, err := d.svc.ListPosts(ctx, tenant, app.ListPostsInput{})
	if err != nil {
		return nil, d.toolError("resource:posts", err)
	}
	return jsonResource(uriPosts, posts)
}

// resourceTenantID reads the tenant from the verified bearer token, exactly
// as the tools do.
func resourceTenantID(req *mcp.ReadResourceRequest) (string, error) {
	if req == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return "", errSessionUnauthenticated
	}
	return req.Extra.TokenInfo.UserID, nil
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("sérialisation de la ressource: %w", err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(payload),
		}},
	}, nil
}
