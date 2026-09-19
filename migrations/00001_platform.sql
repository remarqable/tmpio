-- +goose Up
-- Platform tables: users, identities, sessions, tenants, OAuth clients, login state.
-- These carry no tenant_id and are readable by the runtime role without a tenant scope.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE "user" (
  id           BIGSERIAL PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  email        TEXT NOT NULL,
  avatar_url   TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER set_user_updated_at BEFORE UPDATE ON "user"
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE identity (
  id             BIGSERIAL PRIMARY KEY,
  user_id        BIGINT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  issuer         TEXT NOT NULL,
  subject        TEXT NOT NULL,
  email          TEXT NOT NULL DEFAULT '',
  email_verified BOOLEAN NOT NULL DEFAULT FALSE,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (issuer, subject)
);
CREATE INDEX idx_identity_user ON identity (user_id);

CREATE TABLE session (
  id           BIGSERIAL PRIMARY KEY,
  token_hash   BYTEA NOT NULL UNIQUE,
  user_id      BIGINT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  csrf_token   TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at   TIMESTAMPTZ NOT NULL,
  revoked_at   TIMESTAMPTZ
);
CREATE INDEX idx_session_user ON session (user_id);

CREATE TABLE tenant (
  id         BIGSERIAL PRIMARY KEY,
  code       TEXT NOT NULL UNIQUE,
  name       TEXT,
  generation BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER set_tenant_updated_at BEFORE UPDATE ON tenant
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE oauth_client (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  redirect_uris TEXT[] NOT NULL,
  is_public     BOOLEAN NOT NULL DEFAULT TRUE,
  secret_hash   BYTEA,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER set_oauth_client_updated_at BEFORE UPDATE ON oauth_client
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Pending sign-in state: Google state/nonce/PKCE, the validated return path,
-- and an optional pending client OAuth authorization request.
CREATE TABLE login_state (
  id            BIGSERIAL PRIMARY KEY,
  state_hash    BYTEA NOT NULL UNIQUE,
  nonce         TEXT NOT NULL,
  pkce_verifier TEXT NOT NULL DEFAULT '',
  return_path   TEXT NOT NULL DEFAULT '',
  oauth_request JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at    TIMESTAMPTZ NOT NULL,
  used_at       TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS login_state;
DROP TABLE IF EXISTS oauth_client;
DROP TABLE IF EXISTS tenant CASCADE;
DROP TABLE IF EXISTS session;
DROP TABLE IF EXISTS identity;
DROP TABLE IF EXISTS "user" CASCADE;
DROP FUNCTION IF EXISTS set_updated_at();
