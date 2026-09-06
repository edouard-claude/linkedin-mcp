-- Tenants: one row per LinkedIn member who logged in.
CREATE TABLE tenants (
  id                TEXT PRIMARY KEY,           -- uuid v4
  member_id         TEXT NOT NULL UNIQUE,       -- LinkedIn person id
  display_name      TEXT NOT NULL,
  headline          TEXT,
  access_token_enc  BLOB NOT NULL,              -- AES-256-GCM
  refresh_token_enc BLOB,                       -- usually absent
  token_expires_at  INTEGER NOT NULL DEFAULT 0, -- 0 = unknown
  scopes            TEXT NOT NULL DEFAULT '',   -- space delimited
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);
CREATE INDEX idx_tenants_token_expiry ON tenants(token_expires_at);

-- Ledger of the posts this server published.
--
-- LinkedIn gates reading a member's own posts behind r_member_social, a
-- restricted permission. Recording what we publish is what lets list_posts
-- work for everyone else.
CREATE TABLE posts (
  tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  post_urn   TEXT NOT NULL,
  commentary TEXT,
  visibility TEXT,
  permalink  TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, post_urn)
);
CREATE INDEX idx_posts_tenant_created ON posts(tenant_id, created_at DESC);

-- MCP clients registered through dynamic client registration (RFC 7591).
CREATE TABLE oauth_clients (
  client_id     TEXT PRIMARY KEY,
  client_name   TEXT,
  redirect_uris TEXT NOT NULL,               -- JSON array
  created_at    INTEGER NOT NULL
);

-- Authorization codes, single use, short TTL.
CREATE TABLE oauth_codes (
  code           TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL,
  tenant_id      TEXT NOT NULL,
  redirect_uri   TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  resource       TEXT,
  expires_at     INTEGER NOT NULL
);
CREATE INDEX idx_oauth_codes_expires ON oauth_codes(expires_at);

-- Refresh tokens, stored hashed, rotated on every use.
CREATE TABLE oauth_refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  client_id  TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_oauth_refresh_tenant ON oauth_refresh_tokens(tenant_id);
CREATE INDEX idx_oauth_refresh_expires ON oauth_refresh_tokens(expires_at);

-- CSRF state of the LinkedIn login round trip.
CREATE TABLE login_states (
  state         TEXT PRIMARY KEY,
  oauth_request TEXT NOT NULL,
  expires_at    INTEGER NOT NULL
);
CREATE INDEX idx_login_states_expires ON login_states(expires_at);
