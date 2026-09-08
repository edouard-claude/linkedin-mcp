// Package web holds the minimal HTML pages the OAuth flow needs, embedded in
// the binary so there is nothing to deploy alongside it.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
)

//go:embed *.html
var files embed.FS

//go:embed icon.png
var icon []byte

// Icon is the server's own icon, advertised over MCP and served on /icon.png.
// It ships inside the binary so a client can always fetch it, whatever the
// deployment.
func Icon() []byte { return icon }

// IconHandler serves the icon. It is immutable for the life of a build, so it
// is cached hard.
func IconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		_, _ = w.Write(icon)
	})
}

// Page names, matching the template files.
const (
	PageError       = "error.html"
	PagePrivacy     = "privacy.html"
	PageReconnected = "reconnected.html"
)

// ErrorData fills the error page. Detail is optional.
type ErrorData struct {
	Title   string
	Message string
	Detail  string
}

// ReconnectedData fills the page shown after a successful reconnection.
type ReconnectedData struct {
	Title       string
	DisplayName string
}

// PrivacyData fills the privacy policy page.
type PrivacyData struct {
	Title string
}

// pages holds every page pre-parsed with the shared layout. Each page file
// defines its own "content" block, so they are parsed separately rather than
// all into one template set.
var pages = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, name := range []string{PageError, PagePrivacy, PageReconnected} {
		out[name] = template.Must(template.ParseFS(files, "layout.html", name))
	}
	return out
}()

// Render writes a page. It buffers the render so a template failure cannot
// produce a half written response.
func Render(w http.ResponseWriter, status int, page string, data any) error {
	tmpl, ok := pages[page]
	if !ok {
		return fmt.Errorf("page inconnue: %s", page)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return fmt.Errorf("render %s: %w", page, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}
