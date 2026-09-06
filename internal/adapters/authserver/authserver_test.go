package authserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/adapters/crypto"
	"github.com/edouard-claude/linkedin-mcp/internal/adapters/sqlite"
)

const (
	testIssuer      = "https://mcp.example.re"
	testResource    = testIssuer + "/mcp"
	testRedirectURI = "https://claude.ai/api/mcp/auth_callback"
	testVerifier    = "0123456789abcdefghijklmnopqrstuvwxyz-._~ABCDEF"
)

// fakeClock lets the tests move time forward without sleeping.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type harness struct {
	srv    *Server
	store  *sqlite.Store
	clock  *fakeClock
	router http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cipher, err := crypto.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "auth.db"), cipher)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	clk := &fakeClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
	srv := New(store, clk, Options{
		Issuer:          testIssuer,
		Resource:        testResource,
		SigningKey:      bytes.Repeat([]byte{3}, 32),
		LoginPath:       "/meta/login",
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 720 * time.Hour,
	}, slog.New(slog.DiscardHandler))

	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/oauth-protected-resource", srv.ProtectedResourceMetadataHandler())
	mux.Handle("GET /.well-known/oauth-authorization-server", srv.AuthServerMetadataHandler())
	mux.Handle("POST /oauth/register", srv.RegisterHandler())
	mux.Handle("GET /oauth/authorize", srv.AuthorizeHandler())
	mux.Handle("POST /oauth/token", srv.TokenHandler())

	return &harness{srv: srv, store: store, clock: clk, router: mux}
}

func (h *harness) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// register performs a dynamic client registration and returns the client id.
func (h *harness) register(t *testing.T, redirectURIs ...string) string {
	t.Helper()
	body, err := json.Marshal(registrationRequest{ClientName: "Claude", RedirectURIs: redirectURIs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	rec := h.do(t, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, corps %s", rec.Code, rec.Body.String())
	}
	var resp registrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if resp.ClientID == "" || resp.TokenEndpointAuthMethod != "none" {
		t.Fatalf("réponse d'enregistrement inattendue: %+v", resp)
	}
	return resp.ClientID
}

// authorize walks /oauth/authorize and returns the login state it parked.
func (h *harness) authorize(t *testing.T, clientID, redirectURI, clientState string) string {
	t.Helper()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {challengeFor(testVerifier)},
		"code_challenge_method": {"S256"},
		"state":                 {clientState},
		"resource":              {testResource},
	}
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize: status %d, corps %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location illisible: %v", err)
	}
	if loc.Path != "/meta/login" {
		t.Fatalf("redirection vers %s, attendu /meta/login", loc.Path)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("aucun state transmis à la page de login")
	}
	return state
}

// completeLogin plays the part of the Meta callback: it consumes the login
// state and mints the authorization code for a tenant.
func (h *harness) completeLogin(t *testing.T, state, tenantID string) *url.URL {
	t.Helper()
	login, err := h.store.ConsumeLoginState(t.Context(), state)
	if err != nil {
		t.Fatalf("ConsumeLoginState: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/meta/callback", nil)
	target, err := h.srv.IssueAuthCode(req, login.Request, tenantID)
	if err != nil {
		t.Fatalf("IssueAuthCode: %v", err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("URL de redirection illisible: %v", err)
	}
	return u
}

func (h *harness) postToken(t *testing.T, form url.Values) (*httptest.ResponseRecorder, tokenResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := h.do(t, req)
	var resp tokenResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func codeGrantForm(clientID, code, verifier, redirectURI string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
	}
}

func TestFullAuthorizationCodeFlowWithPKCEAndRefresh(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	state := h.authorize(t, clientID, testRedirectURI, "client-state-42")

	redirect := h.completeLogin(t, state, "tenant-a")
	if redirect.Query().Get("state") != "client-state-42" {
		t.Fatalf("state client non restitué: %s", redirect.RawQuery)
	}
	code := redirect.Query().Get("code")
	if code == "" {
		t.Fatal("aucun code d'autorisation")
	}

	rec, tok := h.postToken(t, codeGrantForm(clientID, code, testVerifier, testRedirectURI))
	if rec.Code != http.StatusOK {
		t.Fatalf("token: status %d, corps %s", rec.Code, rec.Body.String())
	}
	if tok.TokenType != "Bearer" || tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("réponse de jeton incomplète: %+v", tok)
	}
	if tok.ExpiresIn != 3600 {
		t.Fatalf("expires_in = %d, attendu 3600", tok.ExpiresIn)
	}

	claims, err := h.srv.VerifyAccessToken(tok.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
	if claims.TenantID() != "tenant-a" || claims.ClientID != clientID {
		t.Fatalf("claims inattendues: %+v", claims)
	}
	if claims.Issuer != testIssuer || claims.Audience != testResource || claims.JTI == "" {
		t.Fatalf("claims incomplètes: %+v", claims)
	}

	// Refresh: the old token dies, a new pair is issued.
	rec, refreshed := h.postToken(t, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: status %d, corps %s", rec.Code, rec.Body.String())
	}
	if refreshed.RefreshToken == tok.RefreshToken {
		t.Fatal("le refresh token n'a pas tourné")
	}
	newClaims, err := h.srv.VerifyAccessToken(refreshed.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccessToken après refresh: %v", err)
	}
	if newClaims.TenantID() != "tenant-a" {
		t.Fatalf("tenant perdu au refresh: %+v", newClaims)
	}

	// Replaying the consumed refresh token must fail.
	rec, _ = h.postToken(t, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejeu du refresh token: status %d", rec.Code)
	}
}

func TestTokenRejectsWrongVerifier(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	state := h.authorize(t, clientID, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	wrong := strings.Repeat("z", 50)
	rec, _ := h.postToken(t, codeGrantForm(clientID, code, wrong, testRedirectURI))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), errInvalidGrant) {
		t.Fatalf("corps = %s", rec.Body.String())
	}
}

func TestTokenRejectsShortVerifier(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	state := h.authorize(t, clientID, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	rec, _ := h.postToken(t, codeGrantForm(clientID, code, "trop-court", testRedirectURI))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
}

func TestTokenRejectsReusedCode(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	state := h.authorize(t, clientID, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	form := codeGrantForm(clientID, code, testVerifier, testRedirectURI)
	if rec, _ := h.postToken(t, form); rec.Code != http.StatusOK {
		t.Fatalf("premier échange: status %d", rec.Code)
	}
	rec, _ := h.postToken(t, form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejeu du code: status %d, attendu 400", rec.Code)
	}
}

func TestTokenRejectsDifferentRedirectURI(t *testing.T) {
	h := newHarness(t)
	other := "https://claude.ai/autre"
	clientID := h.register(t, testRedirectURI, other)
	state := h.authorize(t, clientID, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	rec, _ := h.postToken(t, codeGrantForm(clientID, code, testVerifier, other))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
}

func TestTokenRejectsOtherClient(t *testing.T) {
	h := newHarness(t)
	clientA := h.register(t, testRedirectURI)
	clientB := h.register(t, testRedirectURI)
	state := h.authorize(t, clientA, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	rec, _ := h.postToken(t, codeGrantForm(clientB, code, testVerifier, testRedirectURI))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
}

func TestTokenRejectsExpiredCode(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	state := h.authorize(t, clientID, testRedirectURI, "s")
	code := h.completeLogin(t, state, "tenant-a").Query().Get("code")

	h.clock.advance(AuthCodeTTL + time.Second)
	rec, _ := h.postToken(t, codeGrantForm(clientID, code, testVerifier, testRedirectURI))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
}

func TestTokenRejectsUnknownGrant(t *testing.T) {
	h := newHarness(t)
	rec, _ := h.postToken(t, url.Values{"grant_type": {"password"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), errUnsupportedGrantType) {
		t.Fatalf("corps = %s", rec.Body.String())
	}
}

func TestAuthorizeRejectsUnknownClient(t *testing.T) {
	h := newHarness(t)
	q := url.Values{
		"response_type": {"code"}, "client_id": {"inconnu"},
		"redirect_uri": {testRedirectURI}, "code_challenge": {challengeFor(testVerifier)},
		"code_challenge_method": {"S256"},
	}
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, attendu 401", rec.Code)
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("une redirection a été émise vers une URI non vérifiée")
	}
}

func TestAuthorizeRejectsUnregisteredRedirectURI(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID},
		"redirect_uri": {"https://evil.example/cb"}, "code_challenge": {challengeFor(testVerifier)},
		"code_challenge_method": {"S256"},
	}
	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, attendu 400", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("redirection ouverte vers %s", loc)
	}
}

func TestAuthorizeRedirectsProtocolErrors(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, testRedirectURI)
	cases := map[string]url.Values{
		"sans PKCE": {
			"response_type": {"code"}, "client_id": {clientID},
			"redirect_uri": {testRedirectURI}, "state": {"st"},
		},
		"méthode PKCE plain": {
			"response_type": {"code"}, "client_id": {clientID},
			"redirect_uri": {testRedirectURI}, "state": {"st"},
			"code_challenge": {testVerifier}, "code_challenge_method": {"plain"},
		},
		"response_type token": {
			"response_type": {"token"}, "client_id": {clientID},
			"redirect_uri": {testRedirectURI}, "state": {"st"},
			"code_challenge": {challengeFor(testVerifier)}, "code_challenge_method": {"S256"},
		},
		"resource étrangère": {
			"response_type": {"code"}, "client_id": {clientID},
			"redirect_uri": {testRedirectURI}, "state": {"st"},
			"code_challenge": {challengeFor(testVerifier)}, "code_challenge_method": {"S256"},
			"resource": {"https://autre.example/mcp"},
		},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			rec := h.do(t, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
			if rec.Code != http.StatusFound {
				t.Fatalf("status %d, attendu 302", rec.Code)
			}
			loc, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatalf("Location: %v", err)
			}
			if loc.Query().Get("error") == "" {
				t.Fatalf("aucune erreur dans la redirection: %s", loc)
			}
			if loc.Query().Get("state") != "st" {
				t.Fatalf("state non restitué: %s", loc)
			}
		})
	}
}

func TestRegisterRejectsBadRedirectURIs(t *testing.T) {
	h := newHarness(t)
	cases := map[string][]string{
		"aucune":           {},
		"http distant":     {"http://evil.example/cb"},
		"schéma inconnu":   {"ftp://example/cb"},
		"avec un fragment": {"https://claude.ai/cb#frag"},
	}
	for name, uris := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(registrationRequest{ClientName: "x", RedirectURIs: uris})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rec := h.do(t, httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, attendu 400 (corps %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRegisterAcceptsLoopbackRedirect(t *testing.T) {
	h := newHarness(t)
	clientID := h.register(t, "http://localhost:6274/oauth/callback", "http://127.0.0.1:33418/cb")
	if clientID == "" {
		t.Fatal("client_id vide")
	}
}

func TestVerifyAccessTokenRejects(t *testing.T) {
	h := newHarness(t)
	token, _, err := h.srv.MintAccessToken("tenant-a", "cid")
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}

	t.Run("expiré", func(t *testing.T) {
		h.clock.advance(2 * time.Hour)
		defer h.clock.advance(-2 * time.Hour)
		if _, err := h.srv.VerifyAccessToken(token); err == nil {
			t.Fatal("un jeton expiré a été accepté")
		}
	})

	t.Run("audience différente", func(t *testing.T) {
		other := New(h.store, h.clock, Options{
			Issuer: testIssuer, Resource: "https://autre.example/mcp",
			SigningKey: bytes.Repeat([]byte{3}, 32), AccessTokenTTL: time.Hour,
		}, slog.New(slog.DiscardHandler))
		if _, err := other.VerifyAccessToken(token); err == nil {
			t.Fatal("un jeton destiné à une autre resource a été accepté")
		}
	})

	t.Run("émetteur différent", func(t *testing.T) {
		other := New(h.store, h.clock, Options{
			Issuer: "https://autre.example", Resource: testResource,
			SigningKey: bytes.Repeat([]byte{3}, 32), AccessTokenTTL: time.Hour,
		}, slog.New(slog.DiscardHandler))
		if _, err := other.VerifyAccessToken(token); err == nil {
			t.Fatal("un jeton d'un autre émetteur a été accepté")
		}
	})

	t.Run("clé de signature différente", func(t *testing.T) {
		other := New(h.store, h.clock, Options{
			Issuer: testIssuer, Resource: testResource,
			SigningKey: bytes.Repeat([]byte{9}, 32), AccessTokenTTL: time.Hour,
		}, slog.New(slog.DiscardHandler))
		if _, err := other.VerifyAccessToken(token); err == nil {
			t.Fatal("une signature invalide a été acceptée")
		}
	})

	t.Run("signature tronquée", func(t *testing.T) {
		if _, err := h.srv.VerifyAccessToken(token[:len(token)-4]); err == nil {
			t.Fatal("une signature tronquée a été acceptée")
		}
	})

	t.Run("alg none", func(t *testing.T) {
		header, _ := encodeSegment(jwtHeader{Alg: "none", Typ: typJWT})
		payload, _ := encodeSegment(Claims{
			Issuer: testIssuer, Subject: "tenant-a", Audience: testResource,
			ExpiresAt: h.clock.Now().Add(time.Hour).Unix(),
		})
		if _, err := h.srv.VerifyAccessToken(header + "." + payload + "."); err == nil {
			t.Fatal("alg=none a été accepté")
		}
	})

	t.Run("malformé", func(t *testing.T) {
		for _, bad := range []string{"", "abc", "a.b", "a.b.c.d"} {
			if _, err := h.srv.VerifyAccessToken(bad); err == nil {
				t.Fatalf("jeton malformé accepté: %q", bad)
			}
		}
	})
}

func TestMetadataDocuments(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var prm protectedResourceMetadata
	decode(t, rec.Body, &prm)
	if prm.Resource != testResource || len(prm.AuthorizationServers) != 1 ||
		prm.AuthorizationServers[0] != testIssuer {
		t.Fatalf("métadonnées de resource inattendues: %+v", prm)
	}
	if len(prm.BearerMethodsSupported) != 1 || prm.BearerMethodsSupported[0] != "header" {
		t.Fatalf("bearer_methods_supported = %v", prm.BearerMethodsSupported)
	}

	rec = h.do(t, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var asm authServerMetadata
	decode(t, rec.Body, &asm)
	if asm.Issuer != testIssuer ||
		asm.AuthorizationEndpoint != testIssuer+"/oauth/authorize" ||
		asm.TokenEndpoint != testIssuer+"/oauth/token" ||
		asm.RegistrationEndpoint != testIssuer+"/oauth/register" {
		t.Fatalf("métadonnées du serveur inattendues: %+v", asm)
	}
	if len(asm.CodeChallengeMethodsSupported) != 1 || asm.CodeChallengeMethodsSupported[0] != "S256" {
		t.Fatalf("PKCE S256 n'est pas annoncé: %v", asm.CodeChallengeMethodsSupported)
	}
	// No jwks_uri must leak into the document: the signing key is symmetric.
	if strings.Contains(rec.Body.String(), "jwks_uri") {
		t.Fatalf("jwks_uri présent dans les métadonnées: %s", rec.Body.String())
	}
}

func decode(t *testing.T, r io.Reader, out any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(out); err != nil {
		t.Fatalf("décodage: %v", err)
	}
}
