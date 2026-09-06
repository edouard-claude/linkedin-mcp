package linkedin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeAPI replays canned LinkedIn answers. Routes are keyed by
// "METHOD /path", with the URN separators decoded so the fixtures stay
// readable.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	routes   map[string]http.HandlerFunc
}

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Form   url.Values
	Body   string
	Header http.Header
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, routes: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) URL() string { return f.server.URL }

func (f *fakeAPI) handle(route string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

// json replies with a body and a 200.
func (f *fakeAPI) json(route, body string) {
	f.handle(route, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// fail replies with a LinkedIn error envelope.
func (f *fakeAPI) fail(route string, status int, message string) {
	f.handle(route, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"` + message + `","status":` + itoa(status) + `}`))
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	// LinkedIn URNs travel percent-encoded; decoding makes the routes
	// readable in the tests.
	path, err := url.PathUnescape(r.URL.EscapedPath())
	if err != nil {
		path = r.URL.Path
	}

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method, Path: path, Query: r.URL.Query(),
		Form: r.PostForm, Body: string(body), Header: r.Header.Clone(),
	})
	handler, ok := f.routes[r.Method+" "+path]
	f.mu.Unlock()

	if !ok {
		f.t.Errorf("route inattendue: %s %s", r.Method, path)
		http.Error(w, `{"message":"unknown route","status":404}`, http.StatusNotFound)
		return
	}
	handler(w, r)
}

// calls returns every recorded request for a path.
func (f *fakeAPI) calls(path string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, req := range f.requests {
		if req.Path == path {
			out = append(out, req)
		}
	}
	return out
}

// newTestClient points a Client at the fake API.
func (f *fakeAPI) newTestClient() *Client {
	return New(Options{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		APIVersion:   "202606",
		APIBase:      f.URL(),
		AuthBase:     f.URL(),
		Scopes:       "openid profile w_member_social",
		RetryDelay:   time.Millisecond,
	})
}
