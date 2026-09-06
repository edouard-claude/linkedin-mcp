package httpserver

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RelayOptions configures the loopback OAuth relay.
type RelayOptions struct {
	// Port is the local port the relay forwards to. Zero disables the route.
	Port int
	// Path is the path on the loopback listener, e.g. "/callback".
	Path string
}

// LoopbackRelayHandler forwards an OAuth callback to a listener on the
// machine that opened the browser.
//
// It exists because Meta refuses to register any loopback redirect URI, in
// http as well as https, so a desktop client that catches its code on
// 127.0.0.1 cannot be named as the redirect target. Registering this public
// route instead, and letting it bounce the query string to the local port,
// is the only shape Meta accepts.
//
// The destination is fixed at startup: the host is always 127.0.0.1 and the
// port comes from the configuration, never from the request. That is what
// keeps this from being an open redirector.
func LoopbackRelayHandler(opts RelayOptions, logger *slog.Logger) http.Handler {
	path := opts.Path
	if path == "" {
		path = "/callback"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	target := url.URL{
		Scheme: "http",
		Host:   "127.0.0.1:" + strconv.Itoa(opts.Port),
		Path:   path,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dest := target
		dest.RawQuery = r.URL.RawQuery
		logger.Info("relais OAuth vers le poste local", "port", opts.Port)
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
}
