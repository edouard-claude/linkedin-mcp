// Package httpserver wires every HTTP surface of the server behind a single
// net/http router. It knows nothing about OAuth, Meta or MCP: the composition
// root injects the handlers it mounts.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps every request body. Nothing this server accepts is large,
// and an unbounded body is free memory for an attacker.
const maxBodyBytes = 1 << 20 // 1 MiB

// Handlers are the endpoints the composition root plugs into the router. A
// nil field is simply not mounted, which keeps the router usable in tests
// that only exercise part of the surface.
type Handlers struct {
	ProtectedResourceMetadata http.Handler
	AuthServerMetadata        http.Handler
	Register                  http.Handler
	Authorize                 http.Handler
	Token                     http.Handler

	LinkedInLogin    http.Handler
	LinkedInCallback http.Handler
	Privacy          http.Handler
	// Icon serves the server's own icon, the one advertised over MCP.
	Icon http.Handler

	MCP http.Handler

	// LoopbackRelay forwards an OAuth callback to a client listening on the
	// user's own machine, which Meta refuses to name as a redirect target.
	LoopbackRelay http.Handler

	// Health reports whether the process can serve traffic. A non-nil error
	// turns GET /healthz into a 503.
	Health func(ctx context.Context) error
}

// New builds the router. The returned handler already applies the body limit,
// the panic guard and the access log.
func New(h Handlers, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthHandler(h.Health))

	mount(mux, "GET /.well-known/oauth-protected-resource", h.ProtectedResourceMetadata)
	// Some MCP clients append the resource path to the well-known URL, as
	// allowed by RFC 9728 §3.1. Both spellings answer the same metadata.
	mount(mux, "GET /.well-known/oauth-protected-resource/mcp", h.ProtectedResourceMetadata)
	mount(mux, "GET /.well-known/oauth-authorization-server", h.AuthServerMetadata)
	mount(mux, "GET /.well-known/oauth-authorization-server/mcp", h.AuthServerMetadata)

	mount(mux, "POST /oauth/register", h.Register)
	mount(mux, "GET /oauth/authorize", h.Authorize)
	mount(mux, "POST /oauth/token", h.Token)

	mount(mux, "GET /linkedin/login", h.LinkedInLogin)
	mount(mux, "GET /linkedin/callback", h.LinkedInCallback)
	mount(mux, "GET /privacy", h.Privacy)
	mount(mux, "GET /icon.png", h.Icon)
	mount(mux, "GET /relay/callback", h.LoopbackRelay)

	if h.MCP != nil {
		// The MCP transport needs POST, GET and DELETE on the same path.
		mux.Handle("/mcp", h.MCP)
	}

	return withAccessLog(logger, withPanicGuard(logger, withLimits(mux)))
}

func mount(mux *http.ServeMux, pattern string, h http.Handler) {
	if h != nil {
		mux.Handle(pattern, h)
	}
}

// healthHandler answers GET /healthz. It exercises the database so an
// unreadable volume shows up as an unhealthy container.
func healthHandler(check func(ctx context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, body := http.StatusOK, map[string]string{"status": "ok"}
		if check != nil {
			if err := check(r.Context()); err != nil {
				status, body = http.StatusServiceUnavailable, map[string]string{"status": "error"}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

// withLimits caps the request body and sets Cache-Control: no-store on every
// endpoint that can carry a credential.
func withLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if isSensitivePath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func isSensitivePath(path string) bool {
	return strings.HasPrefix(path, "/oauth/") ||
		strings.HasPrefix(path, "/linkedin/") ||
		strings.HasPrefix(path, "/mcp")
}

// withPanicGuard keeps a bug in one request from taking the process down.
func withPanicGuard(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				http.Error(w, "erreur interne", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so the MCP SSE stream keeps
// streaming through the middleware chain.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withAccessLog logs one line per request. The query string is deliberately
// never logged: it carries OAuth codes, states and tokens.
func withAccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
