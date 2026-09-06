// Package linkedin talks to the LinkedIn REST API and drives its OAuth flow.
// It is the only place in the codebase that knows LinkedIn URLs, URN shapes
// and error envelopes.
package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

const (
	defaultAPIBase  = "https://api.linkedin.com"
	defaultAuthBase = "https://www.linkedin.com"
	defaultTimeout  = 20 * time.Second
	defaultRetry    = 30 * time.Second
	// maxErrorBody caps how much of an unparseable body reaches an error.
	maxErrorBody = 300
	// maxPages bounds cursor following so a runaway paging cannot loop.
	maxPages = 20
)

// Options configures the client. ClientID and ClientSecret are mandatory; the
// base URLs are overridden by the tests.
type Options struct {
	ClientID     string
	ClientSecret string
	// APIVersion is the LinkedIn-Version header, in YYYYMM form.
	APIVersion  string
	APIBase     string
	AuthBase    string
	RedirectURI string
	Scopes      string
	HTTPClient  *http.Client
	RetryDelay  time.Duration
}

// Client implements domain.Client over the LinkedIn REST API.
type Client struct {
	http        *http.Client
	apiBase     string
	authBase    string
	version     string
	clientID    string
	secret      string
	redirectURI string
	scopes      string
	retryDelay  time.Duration
}

var _ domain.Client = (*Client)(nil)

// New builds a client from the options.
func New(opts Options) *Client {
	c := &Client{
		http:        opts.HTTPClient,
		apiBase:     strings.TrimRight(orDefault(opts.APIBase, defaultAPIBase), "/"),
		authBase:    strings.TrimRight(orDefault(opts.AuthBase, defaultAuthBase), "/"),
		version:     orDefault(opts.APIVersion, "202606"),
		clientID:    opts.ClientID,
		secret:      opts.ClientSecret,
		redirectURI: opts.RedirectURI,
		scopes:      opts.Scopes,
		retryDelay:  opts.RetryDelay,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultTimeout}
	}
	if c.retryDelay == 0 {
		c.retryDelay = defaultRetry
	}
	return c
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ----- OAuth -----

// AuthorizeURL builds the LinkedIn consent page URL.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"scope":         {c.scopes},
	}
	return c.authBase + "/oauth/v2/authorization?" + q.Encode()
}

// tokenResponse is the shape of the token endpoint answer.
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
}

func (r tokenResponse) toToken() domain.Token {
	return domain.Token{
		AccessToken:      r.AccessToken,
		ExpiresIn:        time.Duration(r.ExpiresIn) * time.Second,
		RefreshToken:     r.RefreshToken,
		RefreshExpiresIn: time.Duration(r.RefreshTokenExpiresIn) * time.Second,
		Scopes:           domain.ParseScopes(r.Scope),
	}
}

// ExchangeCode trades an authorization code for an access token. LinkedIn
// issues 60 day tokens, and a refresh token only to approved partners.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (domain.Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {c.clientID},
		"client_secret": {c.secret},
		"redirect_uri":  {redirectURI},
	}
	var resp tokenResponse
	if err := c.oauthForm(ctx, "/oauth/v2/accessToken", form, &resp); err != nil {
		return domain.Token{}, fmt.Errorf("échange du code LinkedIn: %w", err)
	}
	if resp.AccessToken == "" {
		return domain.Token{}, errors.New("échange du code LinkedIn: aucun access_token dans la réponse")
	}
	return resp.toToken(), nil
}

// RefreshAccessToken renews a token without sending the member back through
// the browser. LinkedIn only issues refresh tokens to approved partners, so
// this usually reports that no refresh token exists.
func (c *Client) RefreshAccessToken(ctx context.Context, refreshToken string) (domain.Token, error) {
	if refreshToken == "" {
		return domain.Token{}, domain.ErrRefreshUnavailable
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
		"client_secret": {c.secret},
	}
	var resp tokenResponse
	if err := c.oauthForm(ctx, "/oauth/v2/accessToken", form, &resp); err != nil {
		return domain.Token{}, fmt.Errorf("renouvellement du jeton: %w", err)
	}
	if resp.AccessToken == "" {
		return domain.Token{}, domain.ErrRefreshUnavailable
	}
	return resp.toToken(), nil
}

// IntrospectToken asks LinkedIn what it thinks of a token: whether it is
// active, when it dies, and which permissions it carries.
func (c *Client) IntrospectToken(ctx context.Context, accessToken string) (domain.TokenStatus, error) {
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.secret},
		"token":         {accessToken},
	}
	var resp struct {
		Active    bool   `json:"active"`
		Status    string `json:"status"`
		Scope     string `json:"scope"`
		ExpiresAt int64  `json:"expires_at"`
		AuthType  string `json:"auth_type"`
		ClientID  string `json:"client_id"`
		CreatedAt int64  `json:"created_at"`
	}
	if err := c.oauthForm(ctx, "/oauth/v2/introspectToken", form, &resp); err != nil {
		return domain.TokenStatus{}, fmt.Errorf("diagnostic du jeton: %w", err)
	}

	status := domain.TokenStatus{
		Active: resp.Active,
		Status: resp.Status,
		Scopes: domain.ParseScopes(resp.Scope),
	}
	if resp.ExpiresAt > 0 {
		status.ExpiresAt = time.Unix(resp.ExpiresAt, 0).UTC()
	}
	if !resp.Active {
		status.Reason = resp.Status
	}
	return status, nil
}

// Me returns the identity behind an access token, from the OpenID Connect
// userinfo endpoint that the openid and profile scopes unlock.
func (c *Client) Me(ctx context.Context, accessToken string) (domain.Member, error) {
	var resp struct {
		Sub     string `json:"sub"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture string `json:"picture"`
		Locale  any    `json:"locale"`
	}
	if err := c.do(ctx, request{
		method: http.MethodGet,
		path:   "/v2/userinfo",
		token:  accessToken,
		// userinfo is not a versioned REST resource: sending the version
		// header there is what makes LinkedIn answer 426.
		skipVersion: true,
		out:         &resp,
	}); err != nil {
		return domain.Member{}, fmt.Errorf("lecture du profil LinkedIn: %w", err)
	}
	if resp.Sub == "" {
		return domain.Member{}, errors.New("lecture du profil LinkedIn: identifiant absent")
	}
	return domain.Member{
		ID:      resp.Sub,
		Name:    resp.Name,
		Email:   resp.Email,
		Picture: resp.Picture,
		Locale:  localeString(resp.Locale),
	}, nil
}

// localeString flattens the locale, which LinkedIn returns either as a string
// or as a {country, language} object depending on the endpoint version.
func localeString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		lang, _ := t["language"].(string)
		country, _ := t["country"].(string)
		if lang != "" && country != "" {
			return lang + "_" + country
		}
		return lang
	default:
		return ""
	}
}

// oauthForm posts a form to the OAuth host, which is not the API host and
// takes neither the version header nor a bearer.
func (c *Client) oauthForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.authBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("appel de LinkedIn: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("lecture de la réponse: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFrom(resp, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("décodage de la réponse: %w", err)
	}
	return nil
}

// request describes one call to the versioned REST API.
type request struct {
	method string
	path   string
	token  string
	query  url.Values
	body   any
	// restliMethod fills X-RestLi-Method, which the API needs to tell a
	// finder from a get, or a partial update from a create.
	restliMethod string
	skipVersion  bool
	out          any
	// header receives the response headers when the caller needs
	// x-restli-id, which is where created object ids come back.
	header *http.Header
}

// do performs a REST call, retrying once on a throttle.
func (c *Client) do(ctx context.Context, r request) error {
	err := c.doOnce(ctx, r)
	if err == nil {
		return nil
	}
	var ae *domain.APIError
	if !errors.As(err, &ae) || !ae.IsRateLimit() {
		return err
	}
	delay := ae.RetryAfter
	if delay <= 0 {
		delay = c.retryDelay
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
	}
	return c.doOnce(ctx, r)
}

func (c *Client) doOnce(ctx context.Context, r request) error {
	endpoint := c.apiBase + r.path
	if len(r.query) > 0 {
		endpoint += "?" + r.query.Encode()
	}

	var payload io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("sérialisation du corps: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	if !r.skipVersion {
		req.Header.Set("LinkedIn-Version", c.version)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.restliMethod != "" {
		req.Header.Set("X-RestLi-Method", r.restliMethod)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("appel de LinkedIn: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("lecture de la réponse: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErrorFrom(resp, body)
	}
	if r.header != nil {
		*r.header = resp.Header
	}
	if r.out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, r.out); err != nil {
		return fmt.Errorf("décodage de la réponse: %w", err)
	}
	return nil
}

// apiErrorFrom decodes LinkedIn's error envelope, which differs between the
// OAuth host and the REST API.
func apiErrorFrom(resp *http.Response, body []byte) error {
	ae := &domain.APIError{HTTPStatus: resp.StatusCode}

	var env struct {
		// REST API
		Message          string `json:"message"`
		Status           int    `json:"status"`
		ServiceErrorCode int    `json:"serviceErrorCode"`
		Code             string `json:"code"`
		// OAuth host
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		ae.ServiceErrorCode = env.ServiceErrorCode
		switch {
		case env.Message != "":
			ae.Message, ae.Code = env.Message, env.Code
		case env.ErrorDescription != "":
			ae.Message, ae.Code = env.ErrorDescription, env.Error
		}
	}
	if ae.Message == "" {
		ae.Message = truncate(string(body), maxErrorBody)
	}

	if after := resp.Header.Get("Retry-After"); after != "" {
		if secs, err := strconv.Atoi(after); err == nil && secs > 0 {
			ae.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return ae
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// encodeURN percent-encodes a URN for use in a path segment. LinkedIn needs
// the colons encoded, which url.PathEscape leaves alone.
func encodeURN(urn string) string {
	return strings.NewReplacer(":", "%3A", "(", "%28", ")", "%29", ",", "%2C").Replace(urn)
}
