-- +goose Up
-- Credentials: sharing grants, API tokens, client OAuth grants/tokens/codes.
-- All are tenant-scoped under RLS. Token lookups by hash happen before a tenant
-- is known, so they go through narrow SECURITY DEFINER functions owned by the
-- migration role. The functions return identifiers and status only, never content.

CREATE TABLE share_grant (
  id                 BIGSERIAL PRIMARY KEY,
  tenant_id          BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  entry_id           BIGINT NOT NULL,
  token_hash         BYTEA NOT NULL UNIQUE,
  label              TEXT NOT NULL DEFAULT '',
  created_by_user_id BIGINT REFERENCES "user"(id),
  assets_version     INT NOT NULL DEFAULT 1,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at         TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES entry (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_share_grant_tenant_entry ON share_grant (tenant_id, entry_id);
ALTER TABLE share_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE share_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON share_grant
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE share_grant_asset (
  tenant_id      BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  grant_id       BIGINT NOT NULL,
  asset_entry_id BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, grant_id, asset_entry_id),
  FOREIGN KEY (tenant_id, grant_id) REFERENCES share_grant (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, asset_entry_id) REFERENCES entry (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE share_grant_asset ENABLE ROW LEVEL SECURITY;
ALTER TABLE share_grant_asset FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON share_grant_asset
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE api_token (
  id           BIGSERIAL PRIMARY KEY,
  tenant_id    BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  user_id      BIGINT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  scopes       TEXT[] NOT NULL,
  token_hash   BYTEA NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at   TIMESTAMPTZ NOT NULL,
  revoked_at   TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id)
);
ALTER TABLE api_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_token FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_token
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

-- A grant is one authorized connection between a user, a tenant and a client.
CREATE TABLE oauth_grant (
  id           BIGSERIAL PRIMARY KEY,
  tenant_id    BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  user_id      BIGINT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  client_id    TEXT NOT NULL REFERENCES oauth_client(id),
  scopes       TEXT[] NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at   TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id)
);
ALTER TABLE oauth_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON oauth_grant
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE oauth_code (
  id                    BIGSERIAL PRIMARY KEY,
  tenant_id             BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  grant_id              BIGINT NOT NULL,
  code_hash             BYTEA NOT NULL UNIQUE,
  client_id             TEXT NOT NULL,
  redirect_uri          TEXT NOT NULL,
  resource              TEXT NOT NULL,
  code_challenge        TEXT NOT NULL,
  code_challenge_method TEXT NOT NULL,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at            TIMESTAMPTZ NOT NULL,
  used_at               TIMESTAMPTZ,
  FOREIGN KEY (tenant_id, grant_id) REFERENCES oauth_grant (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE oauth_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_code FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON oauth_code
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE oauth_token (
  id         BIGSERIAL PRIMARY KEY,
  tenant_id  BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  grant_id   BIGINT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
  token_hash BYTEA NOT NULL UNIQUE,
  family_id  TEXT NOT NULL,
  audience   TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  rotated_at TIMESTAMPTZ,
  FOREIGN KEY (tenant_id, grant_id) REFERENCES oauth_grant (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_oauth_token_tenant_grant ON oauth_token (tenant_id, grant_id);
CREATE INDEX idx_oauth_token_tenant_family ON oauth_token (tenant_id, family_id);
ALTER TABLE oauth_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_token FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON oauth_token
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

-- Narrow lookups. Each resolves a hash to identifiers and status before a tenant
-- scope exists. The caller then enters WithTenant for everything else.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lookup_share_grant(p_hash BYTEA)
RETURNS TABLE (grant_id BIGINT, tenant_id BIGINT, entry_id BIGINT, revoked BOOLEAN)
LANGUAGE sql SECURITY DEFINER SET search_path = public STABLE AS $$
  SELECT id, tenant_id, entry_id, revoked_at IS NOT NULL FROM share_grant WHERE token_hash = p_hash
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lookup_api_token(p_hash BYTEA)
RETURNS TABLE (token_id BIGINT, tenant_id BIGINT, user_id BIGINT, scopes TEXT[], expires_at TIMESTAMPTZ, revoked BOOLEAN)
LANGUAGE sql SECURITY DEFINER SET search_path = public STABLE AS $$
  SELECT id, tenant_id, user_id, scopes, expires_at, revoked_at IS NOT NULL FROM api_token WHERE token_hash = p_hash
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lookup_oauth_token(p_hash BYTEA)
RETURNS TABLE (token_id BIGINT, tenant_id BIGINT, grant_id BIGINT, kind TEXT, family_id TEXT, audience TEXT,
               expires_at TIMESTAMPTZ, revoked BOOLEAN, rotated BOOLEAN,
               user_id BIGINT, client_id TEXT, scopes TEXT[], grant_revoked BOOLEAN)
LANGUAGE sql SECURITY DEFINER SET search_path = public STABLE AS $$
  SELECT t.id, t.tenant_id, t.grant_id, t.kind, t.family_id, t.audience,
         t.expires_at, t.revoked_at IS NOT NULL, t.rotated_at IS NOT NULL,
         g.user_id, g.client_id, g.scopes, g.revoked_at IS NOT NULL
    FROM oauth_token t JOIN oauth_grant g ON g.id = t.grant_id AND g.tenant_id = t.tenant_id
   WHERE t.token_hash = p_hash
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION lookup_oauth_code(p_hash BYTEA)
RETURNS TABLE (code_id BIGINT, tenant_id BIGINT, grant_id BIGINT, client_id TEXT, redirect_uri TEXT, resource TEXT,
               code_challenge TEXT, code_challenge_method TEXT, expires_at TIMESTAMPTZ, used BOOLEAN)
LANGUAGE sql SECURITY DEFINER SET search_path = public STABLE AS $$
  SELECT id, tenant_id, grant_id, client_id, redirect_uri, resource, code_challenge, code_challenge_method,
         expires_at, used_at IS NOT NULL
    FROM oauth_code WHERE code_hash = p_hash
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS lookup_oauth_code(BYTEA);
DROP FUNCTION IF EXISTS lookup_oauth_token(BYTEA);
DROP FUNCTION IF EXISTS lookup_api_token(BYTEA);
DROP FUNCTION IF EXISTS lookup_share_grant(BYTEA);
DROP TABLE IF EXISTS oauth_token;
DROP TABLE IF EXISTS oauth_code;
DROP TABLE IF EXISTS oauth_grant;
DROP TABLE IF EXISTS api_token;
DROP TABLE IF EXISTS share_grant_asset;
DROP TABLE IF EXISTS share_grant;
