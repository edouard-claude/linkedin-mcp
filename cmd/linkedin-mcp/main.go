// Command linkedin-mcp is a multi-tenant remote MCP server for personal
// LinkedIn accounts.
//
// It exposes an MCP endpoint over Streamable HTTP at /mcp, protected by its
// own OAuth 2.1 authorization server, and federates the member login to
// LinkedIn.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/adapters/authserver"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/clock"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/crypto"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/httpserver"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/linkedin"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/mcpserver"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/sqlite"
	"github.com/edouard-claude/linkedin-mcp/internal/app"
	"github.com/edouard-claude/linkedin-mcp/internal/config"
)

const (
	shutdownGrace = 10 * time.Second
	purgeInterval = 10 * time.Minute
	// tokenCheckInterval is how often tokens close to expiry are looked at.
	// LinkedIn rarely lets us renew one, so this mostly produces the warning
	// that a member has to reconnect.
	tokenCheckInterval = 12 * time.Hour
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linkedin-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogFormat)

	cipher, err := crypto.New(cfg.TokenCipherKey)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := sqlite.New(ctx, cfg.DBPath, cipher)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.PurgeExpired(ctx, time.Now()); err != nil {
		return fmt.Errorf("purge au démarrage: %w", err)
	}
	go purgeLoop(ctx, store, logger)

	auth := authserver.New(store, clock.System{}, authserver.Options{
		Issuer:          cfg.PublicURL,
		Resource:        cfg.MCPResourceURL(),
		SigningKey:      cfg.JWTSigningKey,
		LoginPath:       "/linkedin/login",
		AccessTokenTTL:  cfg.AccessTokenTTL,
		RefreshTokenTTL: cfg.RefreshTokenTTL,
	}, logger)

	api := linkedin.New(linkedin.Options{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		APIVersion:   cfg.APIVersion,
		APIBase:      cfg.APIBaseURL,
		AuthBase:     cfg.AuthBaseURL,
		RedirectURI:  cfg.RedirectURI(),
		Scopes:       cfg.Scopes,
	})

	login := app.NewLoginService(store, api, clock.System{}, cfg.IsMemberAllowed)
	go tokenLoop(ctx, login, logger)

	handlers := linkedin.NewHandlers(login, auth, linkedin.HandlerOptions{
		PublicURL:   cfg.PublicURL,
		RedirectURI: cfg.RedirectURI(),
	}, logger)

	svc := app.NewService(store, api, clock.System{}, cfg.PublicURL, cfg.ScopeList())
	mcpHandler := mcpserver.Handler(
		mcpserver.New(svc, logger),
		func(token string) (string, time.Time, error) {
			claims, err := auth.VerifyAccessToken(token)
			if err != nil {
				return "", time.Time{}, err
			}
			return claims.TenantID(), claims.Expiry(), nil
		},
		mcpserver.HandlerOptions{ResourceMetadataURL: cfg.ResourceMetadataURL()},
		logger,
	)

	handler := httpserver.New(httpserver.Handlers{
		ProtectedResourceMetadata: auth.ProtectedResourceMetadataHandler(),
		AuthServerMetadata:        auth.AuthServerMetadataHandler(),
		Register:                  auth.RegisterHandler(),
		Authorize:                 auth.AuthorizeHandler(),
		Token:                     auth.TokenHandler(),
		LinkedInLogin:             handlers.LoginHandler(),
		LinkedInCallback:          handlers.CallbackHandler(),
		Privacy:                   handlers.PrivacyHandler(),
		MCP:                       mcpHandler,
		LoopbackRelay:             relayHandler(cfg, logger),
		Health:                    store.Ping,
	}, logger)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("serveur démarré", "addr", cfg.ListenAddr, "public_url", cfg.PublicURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("écoute HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("arrêt demandé, fermeture en cours")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("arrêt du serveur: %w", err)
	}
	return nil
}

// relayHandler builds the loopback OAuth relay, or nil when no port is set.
func relayHandler(cfg *config.Config, logger *slog.Logger) http.Handler {
	if cfg.RelayPort == 0 {
		return nil
	}
	return httpserver.LoopbackRelayHandler(
		httpserver.RelayOptions{Port: cfg.RelayPort, Path: "/callback"}, logger)
}

// purgeLoop sweeps expired short-lived rows until the context is cancelled.
func purgeLoop(ctx context.Context, store *sqlite.Store, logger *slog.Logger) {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.PurgeExpired(ctx, time.Now()); err != nil {
				logger.Error("purge périodique", "error", err)
			}
		}
	}
}

// tokenLoop watches the LinkedIn access tokens that are about to expire.
//
// LinkedIn only hands refresh tokens to approved partners, so for most apps
// this cannot fix anything: it warns, so the operator can tell members to
// reconnect before their sixty days are up.
func tokenLoop(ctx context.Context, login *app.LoginService, logger *slog.Logger) {
	sweep := func() {
		report, err := login.RefreshExpiringTokens(ctx, app.DefaultRefreshWindow)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("suivi des jetons LinkedIn", "error", err)
			}
			return
		}
		if report.Checked == 0 {
			return
		}
		logger.Info("jetons LinkedIn examinés",
			"examines", report.Checked,
			"renouveles", report.Refreshed,
			"a_reconnecter", len(report.NeedsReconnect))
		for _, tenantID := range report.NeedsReconnect {
			logger.Warn("jeton LinkedIn bientôt expiré, reconnexion requise", "tenant_id", tenantID)
		}
	}

	sweep()
	ticker := time.NewTicker(tokenCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// newLogger builds the structured logger: JSON in production, text when
// LOG_FORMAT=text makes local output readable.
func newLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
