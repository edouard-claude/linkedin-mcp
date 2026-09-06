package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// Store is the SQLite implementation of domain.TenantStore. It owns the
// cipher used to seal Meta tokens, so the rest of the application only ever
// sees plaintext tokens in memory and ciphertext on disk.
type Store struct {
	db     *sql.DB
	cipher domain.TokenCipher
}

var _ domain.TenantStore = (*Store)(nil)

// New opens the database at path, creates the parent directory if needed,
// applies the migrations and returns a ready Store.
func New(ctx context.Context, path string, cipher domain.TokenCipher) (*Store, error) {
	if dir := DBPathDir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db directory %s: %w", dir, err)
		}
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, cipher: cipher}, nil
}

// Ping checks that the database is reachable; it backs GET /healthz.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// ----- tenants -----

const tenantColumns = `SELECT id, member_id, display_name, headline,
	access_token_enc, refresh_token_enc, token_expires_at, scopes,
	created_at, updated_at`

// UpsertTenant creates the tenant or refreshes it, keying on the LinkedIn
// member id so a second login reuses the same tenant and the MCP clients
// already authorized against that subject keep working.
func (s *Store) UpsertTenant(ctx context.Context, t *domain.Tenant) error {
	access, err := s.cipher.Encrypt(t.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	var refresh []byte
	if t.RefreshToken != "" {
		if refresh, err = s.cipher.Encrypt(t.RefreshToken); err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
	}

	const q = `INSERT INTO tenants
		(id, member_id, display_name, headline, access_token_enc, refresh_token_enc,
		 token_expires_at, scopes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(member_id) DO UPDATE SET
			display_name      = excluded.display_name,
			headline          = excluded.headline,
			access_token_enc  = excluded.access_token_enc,
			refresh_token_enc = excluded.refresh_token_enc,
			token_expires_at  = excluded.token_expires_at,
			scopes            = excluded.scopes,
			updated_at        = excluded.updated_at`
	if _, err := s.db.ExecContext(ctx, q,
		t.ID, t.MemberID, t.DisplayName, nullable(t.Headline), access, refresh,
		unixOrZero(t.TokenExpiresAt), strings.Join(t.Scopes, " "),
		t.CreatedAt.Unix(), t.UpdatedAt.Unix(),
	); err != nil {
		return fmt.Errorf("upsert tenant: %w", err)
	}
	return nil
}

// unixOrZero stores the zero time as 0 rather than a negative epoch.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// timeOrZero is the inverse of unixOrZero.
func timeOrZero(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func (s *Store) TenantByID(ctx context.Context, id string) (*domain.Tenant, error) {
	return s.tenantBy(ctx, tenantColumns+` FROM tenants WHERE id = ?`, id)
}

func (s *Store) TenantByMemberID(ctx context.Context, memberID string) (*domain.Tenant, error) {
	return s.tenantBy(ctx, tenantColumns+` FROM tenants WHERE member_id = ?`, memberID)
}

func (s *Store) tenantBy(ctx context.Context, query string, arg any) (*domain.Tenant, error) {
	t, err := s.scanTenant(s.db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return t, err
}

func (s *Store) scanTenant(sc scanner) (*domain.Tenant, error) {
	var (
		t                               domain.Tenant
		headline, scopes                sql.NullString
		access, refresh                 []byte
		expiresAt, createdAt, updatedAt int64
	)
	if err := sc.Scan(&t.ID, &t.MemberID, &t.DisplayName, &headline,
		&access, &refresh, &expiresAt, &scopes, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan tenant: %w", err)
	}

	token, err := s.cipher.Decrypt(access)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	t.AccessToken = token
	if len(refresh) > 0 {
		if t.RefreshToken, err = s.cipher.Decrypt(refresh); err != nil {
			return nil, fmt.Errorf("decrypt refresh token: %w", err)
		}
	}
	t.Headline = headline.String
	t.Scopes = domain.ParseScopes(scopes.String)
	t.TokenExpiresAt = timeOrZero(expiresAt)
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &t, nil
}

// TenantsDueForTokenRefresh lists the tenants to renew: those whose token
// dies before expiringBefore, and those with no known deadline not looked at
// since uncheckedBefore.
func (s *Store) TenantsDueForTokenRefresh(ctx context.Context, expiringBefore, uncheckedBefore time.Time) ([]domain.Tenant, error) {
	const q = tenantColumns + ` FROM tenants
		WHERE (token_expires_at > 0 AND token_expires_at < ?)
		   OR (token_expires_at = 0 AND updated_at < ?)
		ORDER BY token_expires_at`
	rows, err := s.db.QueryContext(ctx, q, expiringBefore.Unix(), uncheckedBefore.Unix())
	if err != nil {
		return nil, fmt.Errorf("select tenants to refresh: %w", err)
	}
	defer rows.Close()

	tenants := []domain.Tenant{}
	for rows.Next() {
		t, err := s.scanTenant(rows)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return tenants, nil
}

// DeleteTenant removes a tenant and everything attached to it.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete tenant: %w", err)
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM oauth_refresh_tokens WHERE tenant_id = ?`,
		`DELETE FROM oauth_codes WHERE tenant_id = ?`,
		`DELETE FROM posts WHERE tenant_id = ?`,
		`DELETE FROM tenants WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete tenant: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete tenant: %w", err)
	}
	return nil
}

// ----- post ledger -----

// RecordPost appends a published post to the ledger.
func (s *Store) RecordPost(ctx context.Context, tenantID string, p domain.LedgerPost) error {
	const q = `INSERT INTO posts
		(tenant_id, post_urn, commentary, visibility, permalink, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0)
		ON CONFLICT(tenant_id, post_urn) DO UPDATE SET
			commentary = excluded.commentary,
			visibility = excluded.visibility,
			permalink  = excluded.permalink`
	if _, err := s.db.ExecContext(ctx, q, tenantID, p.PostURN,
		nullable(p.Commentary), nullable(p.Visibility), nullable(p.Permalink),
		p.CreatedAt.Unix()); err != nil {
		return fmt.Errorf("record post: %w", err)
	}
	return nil
}

// MarkPostDeleted records that a post was removed, keeping the row so the
// history stays honest about what was published and then taken down.
func (s *Store) MarkPostDeleted(ctx context.Context, tenantID, postURN string, at time.Time) error {
	const q = `UPDATE posts SET deleted_at = ? WHERE tenant_id = ? AND post_urn = ?`
	if _, err := s.db.ExecContext(ctx, q, at.Unix(), tenantID, postURN); err != nil {
		return fmt.Errorf("mark post deleted: %w", err)
	}
	return nil
}

// UpdateLedgerCommentary keeps the ledger in step with an edited post.
func (s *Store) UpdateLedgerCommentary(ctx context.Context, tenantID, postURN, commentary string, at time.Time) error {
	const q = `UPDATE posts SET commentary = ?, updated_at = ? WHERE tenant_id = ? AND post_urn = ?`
	if _, err := s.db.ExecContext(ctx, q, commentary, at.Unix(), tenantID, postURN); err != nil {
		return fmt.Errorf("update ledger commentary: %w", err)
	}
	return nil
}

// LedgerPosts lists the recorded posts of a tenant, newest first.
func (s *Store) LedgerPosts(ctx context.Context, tenantID string, includeDeleted bool, limit int) ([]domain.LedgerPost, error) {
	q := `SELECT post_urn, commentary, visibility, permalink, created_at, updated_at, deleted_at
		FROM posts WHERE tenant_id = ?`
	if !includeDeleted {
		q += ` AND deleted_at = 0`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("select posts: %w", err)
	}
	defer rows.Close()

	posts := []domain.LedgerPost{}
	for rows.Next() {
		p, err := scanLedgerPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate posts: %w", err)
	}
	return posts, nil
}

// LedgerPost loads one recorded post of one tenant. A URN belonging to
// another tenant yields ErrNotFound, which is the isolation guarantee.
func (s *Store) LedgerPost(ctx context.Context, tenantID, postURN string) (*domain.LedgerPost, error) {
	const q = `SELECT post_urn, commentary, visibility, permalink, created_at, updated_at, deleted_at
		FROM posts WHERE tenant_id = ? AND post_urn = ?`
	p, err := scanLedgerPost(s.db.QueryRowContext(ctx, q, tenantID, postURN))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return p, err
}

func scanLedgerPost(sc scanner) (*domain.LedgerPost, error) {
	var (
		p                                 domain.LedgerPost
		commentary, visibility, permalink sql.NullString
		createdAt, updatedAt, deletedAt   int64
	)
	if err := sc.Scan(&p.PostURN, &commentary, &visibility, &permalink,
		&createdAt, &updatedAt, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan post: %w", err)
	}
	p.Commentary = commentary.String
	p.Visibility = visibility.String
	p.Permalink = permalink.String
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = timeOrZero(updatedAt)
	p.DeletedAt = timeOrZero(deletedAt)
	return &p, nil
}

// ----- oauth clients -----

// RegisterClient stores a dynamically registered MCP client.
func (s *Store) RegisterClient(ctx context.Context, c *domain.OAuthClient) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return fmt.Errorf("marshal redirect_uris: %w", err)
	}
	const q = `INSERT INTO oauth_clients (client_id, client_name, redirect_uris, created_at)
		VALUES (?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q, c.ClientID, c.ClientName, string(uris), c.CreatedAt.Unix()); err != nil {
		return fmt.Errorf("insert oauth client: %w", err)
	}
	return nil
}

// ClientByID loads a registered client.
func (s *Store) ClientByID(ctx context.Context, clientID string) (*domain.OAuthClient, error) {
	const q = `SELECT client_id, client_name, redirect_uris, created_at FROM oauth_clients WHERE client_id = ?`
	var (
		c         domain.OAuthClient
		name      sql.NullString
		uris      string
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx, q, clientID).Scan(&c.ClientID, &name, &uris, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select oauth client: %w", err)
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, fmt.Errorf("decode redirect_uris: %w", err)
	}
	c.ClientName = name.String
	c.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &c, nil
}

// ----- authorization codes -----

// CreateAuthCode stores a freshly minted authorization code.
func (s *Store) CreateAuthCode(ctx context.Context, c *domain.AuthCode) error {
	const q = `INSERT INTO oauth_codes
		(code, client_id, tenant_id, redirect_uri, code_challenge, resource, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q,
		c.Code, c.ClientID, c.TenantID, c.RedirectURI, c.CodeChallenge, nullable(c.Resource), c.ExpiresAt.Unix(),
	); err != nil {
		return fmt.Errorf("insert auth code: %w", err)
	}
	return nil
}

// ConsumeAuthCode deletes the code and returns it in one statement, so a
// replay of the same code finds nothing. Expiry is checked by the caller
// against the returned deadline.
func (s *Store) ConsumeAuthCode(ctx context.Context, code string) (*domain.AuthCode, error) {
	const q = `DELETE FROM oauth_codes WHERE code = ?
		RETURNING code, client_id, tenant_id, redirect_uri, code_challenge, resource, expires_at`
	var (
		c         domain.AuthCode
		resource  sql.NullString
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx, q, code).
		Scan(&c.Code, &c.ClientID, &c.TenantID, &c.RedirectURI, &c.CodeChallenge, &resource, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("consume auth code: %w", err)
	}
	c.Resource = resource.String
	c.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return &c, nil
}

// ----- refresh tokens -----

// CreateRefreshToken stores the SHA-256 of a refresh token.
func (s *Store) CreateRefreshToken(ctx context.Context, rt *domain.RefreshToken) error {
	const q = `INSERT INTO oauth_refresh_tokens (token_hash, client_id, tenant_id, expires_at, revoked)
		VALUES (?, ?, ?, ?, 0)`
	if _, err := s.db.ExecContext(ctx, q, rt.TokenHash, rt.ClientID, rt.TenantID, rt.ExpiresAt.Unix()); err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

// RotateRefreshToken marks the presented token revoked and returns it. A
// token that is already revoked, expired or unknown yields ErrNotFound, so a
// replayed refresh token is simply rejected.
func (s *Store) RotateRefreshToken(ctx context.Context, tokenHash string, now time.Time) (*domain.RefreshToken, error) {
	const q = `UPDATE oauth_refresh_tokens SET revoked = 1
		WHERE token_hash = ? AND revoked = 0 AND expires_at > ?
		RETURNING token_hash, client_id, tenant_id, expires_at`
	var (
		rt        domain.RefreshToken
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx, q, tokenHash, now.Unix()).
		Scan(&rt.TokenHash, &rt.ClientID, &rt.TenantID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: %w", err)
	}
	rt.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	rt.Revoked = true
	return &rt, nil
}

// RevokeTenantRefreshTokens kills every live session of a tenant.
func (s *Store) RevokeTenantRefreshTokens(ctx context.Context, tenantID string) error {
	const q = `UPDATE oauth_refresh_tokens SET revoked = 1 WHERE tenant_id = ?`
	if _, err := s.db.ExecContext(ctx, q, tenantID); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}
	return nil
}

// ----- login states -----

// CreateLoginState parks an MCP authorization request for the duration of the
// Facebook login.
func (s *Store) CreateLoginState(ctx context.Context, st *domain.LoginState) error {
	payload, err := json.Marshal(st.Request)
	if err != nil {
		return fmt.Errorf("marshal oauth request: %w", err)
	}
	const q = `INSERT INTO login_states (state, oauth_request, expires_at) VALUES (?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q, st.State, string(payload), st.ExpiresAt.Unix()); err != nil {
		return fmt.Errorf("insert login state: %w", err)
	}
	return nil
}

// ConsumeLoginState deletes and returns a login state, making it single use.
func (s *Store) ConsumeLoginState(ctx context.Context, state string) (*domain.LoginState, error) {
	const q = `DELETE FROM login_states WHERE state = ? RETURNING state, oauth_request, expires_at`
	var (
		st        domain.LoginState
		payload   string
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx, q, state).Scan(&st.State, &payload, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("consume login state: %w", err)
	}
	if err := json.Unmarshal([]byte(payload), &st.Request); err != nil {
		return nil, fmt.Errorf("decode oauth request: %w", err)
	}
	st.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return &st, nil
}

// ----- housekeeping -----

// PurgeExpired drops every short-lived row that is past its deadline. It runs
// at startup and on a ticker.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) error {
	statements := []string{
		`DELETE FROM oauth_codes WHERE expires_at <= ?`,
		`DELETE FROM login_states WHERE expires_at <= ?`,
		`DELETE FROM oauth_refresh_tokens WHERE expires_at <= ?`,
	}
	for _, q := range statements {
		if _, err := s.db.ExecContext(ctx, q, now.Unix()); err != nil {
			return fmt.Errorf("purge expired: %w", err)
		}
	}
	return nil
}

// nullable turns an empty string into a SQL NULL, keeping optional columns
// genuinely empty rather than holding "".
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
