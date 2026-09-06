package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TenantResolver verifies a bearer token and returns the tenant it authorizes
// with its expiry. It is a function so this adapter never imports the
// authorization server.
type TenantResolver func(token string) (tenantID string, expiry time.Time, err error)

// HandlerOptions configures the MCP HTTP endpoint.
type HandlerOptions struct {
	// ResourceMetadataURL is advertised in the WWW-Authenticate header of a
	// 401. It is what makes an MCP client start the OAuth flow.
	ResourceMetadataURL string
}

// Handler mounts the MCP server on Streamable HTTP behind bearer token
// verification.
//
// Sessions are stateless: nothing about a client is kept between requests, so
// a restart of the container never invalidates a token that has not expired.
func Handler(srv *mcp.Server, resolve TenantResolver, opts HandlerOptions, logger *slog.Logger) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless: true,
			Logger:    logger,
		},
	)

	middleware := auth.RequireBearerToken(verifier(resolve), &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: opts.ResourceMetadataURL,
	})
	return middleware(streamable)
}

// verifier adapts a TenantResolver to the SDK's token verifier. The tenant id
// travels in TokenInfo.UserID, which the transport hands to every tool
// handler and also uses to pin a session to a single user.
func verifier(resolve TenantResolver) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		tenantID, expiry, err := resolve(token)
		if err != nil {
			// Any verification failure is an invalid token: the client must
			// re-run the OAuth flow, and no detail is disclosed.
			return nil, fmt.Errorf("%w: %s", auth.ErrInvalidToken, "jeton d'accès invalide ou expiré")
		}
		return &auth.TokenInfo{
			UserID:     tenantID,
			Expiration: expiry,
		}, nil
	}
}
