-- +goose Up
-- A password credential for self-hosted instances, so that signing in needs no
-- external identity provider. One row per user; the identity row it belongs to
-- has issuer 'local' and the email address as its subject. The hash is argon2id
-- with its parameters encoded in the string, so the cost can be raised later
-- without a migration.
CREATE TABLE local_credential (
  user_id       BIGINT PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER set_local_credential_updated_at BEFORE UPDATE ON local_credential
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS set_local_credential_updated_at ON local_credential;
DROP TABLE IF EXISTS local_credential;
