-- +goose Up
-- Instance-wide settings, owned by the operator rather than by any tenant. The
-- model key is one key for the whole installation: it pays for the calls, and
-- each organization still decides separately whether its content may be sent
-- (tenant.ai_filing_enabled, migration 9).
--
-- The key is stored so the server can replay it, which no hash allows. A
-- database dump therefore contains a usable credential; see docs/SECURITY.md.
CREATE TABLE instance_setting (
  id              SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  ai_api_key      TEXT NOT NULL DEFAULT '',
  ai_model        TEXT NOT NULL DEFAULT '',
  ai_base_url     TEXT NOT NULL DEFAULT '',
  ai_workspace_id TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER set_instance_setting_updated_at BEFORE UPDATE ON instance_setting
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO instance_setting (id) VALUES (1);

-- Every owner owns their own organization. Instance settings belong to whoever
-- installed the server, which is a different thing and needs saying explicitly.
ALTER TABLE "user" ADD COLUMN is_instance_admin BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE "user" DROP COLUMN is_instance_admin;
DROP TRIGGER IF EXISTS set_instance_setting_updated_at ON instance_setting;
DROP TABLE IF EXISTS instance_setting;
