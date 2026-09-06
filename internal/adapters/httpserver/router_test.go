package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "corps trop volumineux", http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = w.Write(body)
	})
}

func newRouter(t *testing.T, h Handlers) http.Handler {
	t.Helper()
	return New(h, slog.New(slog.DiscardHandler))
}

func TestHealthzReportsTheDatabase(t *testing.T) {
	router := newRouter(t, Handlers{Health: func(context.Context) error { return nil }})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("status %d, corps %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestHealthzFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	router := newRouter(t, Handlers{
		Health: func(context.Context) error { return errors.New("base fermée") },
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, attendu 503", rec.Code)
	}
	// The reason must not leak to an anonymous prober.
	if strings.Contains(rec.Body.String(), "base fermée") {
		t.Fatalf("corps = %s", rec.Body.String())
	}
}

func TestNilHandlersAreNotMounted(t *testing.T) {
	router := newRouter(t, Handlers{})
	for _, path := range []string{"/oauth/authorize", "/linkedin/login", "/mcp", "/privacy"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, attendu 404", path, rec.Code)
		}
	}
}

func TestSensitivePathsAreNeverCached(t *testing.T) {
	router := newRouter(t, Handlers{
		Authorize:     echoHandler(),
		LinkedInLogin: echoHandler(),
		MCP:           echoHandler(),
		Privacy:       echoHandler(),
	})
	cases := map[string]string{
		"/oauth/authorize": "no-store",
		"/linkedin/login":  "no-store",
		"/mcp":             "no-store",
		"/privacy":         "", // a static page may be cached
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Fatalf("%s: Cache-Control = %q, attendu %q", path, got, want)
		}
	}
}

func TestBodyIsCapped(t *testing.T) {
	router := newRouter(t, Handlers{MCP: echoHandler()})

	small := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", 1024)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, small)
	if rec.Code != http.StatusOK {
		t.Fatalf("corps normal: status %d", rec.Code)
	}

	huge := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", maxBodyBytes+1)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corps trop gros: status %d, attendu 413", rec.Code)
	}
}

func TestPanicIsContained(t *testing.T) {
	router := newRouter(t, Handlers{
		MCP: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boum") }),
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, attendu 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boum") {
		t.Fatalf("le message de panique a fuité: %s", rec.Body.String())
	}
}

func TestWellKnownAcceptsBothSpellings(t *testing.T) {
	router := newRouter(t, Handlers{
		ProtectedResourceMetadata: echoHandler(),
		AuthServerMetadata:        echoHandler(),
	})
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/.well-known/oauth-authorization-server/mcp",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
	}
}

func TestStatusRecorderForwardsFlush(t *testing.T) {
	// The MCP transport streams: the middleware must not swallow Flush.
	flushed := false
	rec := &statusRecorder{ResponseWriter: &flushRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFlush:          func() { flushed = true },
	}, status: http.StatusOK}
	rec.Flush()
	if !flushed {
		t.Fatal("Flush n'a pas été transmis")
	}
}

// flushRecorder is an http.ResponseWriter that reports being flushed.
type flushRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (f *flushRecorder) Flush() { f.onFlush() }

func TestLoopbackRelayForwardsToTheFixedLocalPort(t *testing.T) {
	router := newRouter(t, Handlers{
		LoopbackRelay: LoopbackRelayHandler(
			RelayOptions{Port: 8787, Path: "/callback"}, slog.New(slog.DiscardHandler)),
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/relay/callback?code=abc&state=xyz", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, attendu 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "http://127.0.0.1:8787/callback?code=abc&state=xyz" {
		t.Fatalf("redirection = %q", loc)
	}
}

// TestLoopbackRelayIsNotAnOpenRedirector pins the property that makes this
// route safe: the destination host and port come from the configuration, so
// nothing in the request can steer it elsewhere.
func TestLoopbackRelayIsNotAnOpenRedirector(t *testing.T) {
	router := newRouter(t, Handlers{
		LoopbackRelay: LoopbackRelayHandler(
			RelayOptions{Port: 8787, Path: "/callback"}, slog.New(slog.DiscardHandler)),
	})
	for _, q := range []string{
		"?redirect_uri=https://evil.example/steal&code=abc",
		"?host=evil.example",
		"?code=abc#@evil.example",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/relay/callback"+q, nil))
		loc := rec.Header().Get("Location")
		if !strings.HasPrefix(loc, "http://127.0.0.1:8787/callback?") {
			t.Fatalf("%s -> %q", q, loc)
		}
	}
}

func TestLoopbackRelayIsAbsentWithoutAPort(t *testing.T) {
	router := newRouter(t, Handlers{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/relay/callback", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, attendu 404", rec.Code)
	}
}
