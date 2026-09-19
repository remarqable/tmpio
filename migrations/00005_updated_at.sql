-- +goose Up
-- Standard updated_at column and trigger on every table whose rows are mutated
-- after insert (revocation, use, rotation, refresh). Immutable ledgers
-- (revision, audit_event, asset_blob, path_alias, mutation_receipt, identity) stay as they are.
ALTER TABLE session     ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE login_state ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE membership  ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE share_grant ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE api_token   ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE oauth_grant ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE oauth_code  ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE oauth_token ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE TRIGGER set_session_updated_at     BEFORE UPDATE ON session     FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_login_state_updated_at BEFORE UPDATE ON login_state FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_membership_updated_at  BEFORE UPDATE ON membership  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_share_grant_updated_at BEFORE UPDATE ON share_grant FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_api_token_updated_at   BEFORE UPDATE ON api_token   FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_oauth_grant_updated_at BEFORE UPDATE ON oauth_grant FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_oauth_code_updated_at  BEFORE UPDATE ON oauth_code  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER set_oauth_token_updated_at BEFORE UPDATE ON oauth_token FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS set_oauth_token_updated_at ON oauth_token;
DROP TRIGGER IF EXISTS set_oauth_code_updated_at  ON oauth_code;
DROP TRIGGER IF EXISTS set_oauth_grant_updated_at ON oauth_grant;
DROP TRIGGER IF EXISTS set_api_token_updated_at   ON api_token;
DROP TRIGGER IF EXISTS set_share_grant_updated_at ON share_grant;
DROP TRIGGER IF EXISTS set_membership_updated_at  ON membership;
DROP TRIGGER IF EXISTS set_login_state_updated_at ON login_state;
DROP TRIGGER IF EXISTS set_session_updated_at     ON session;
ALTER TABLE oauth_token DROP COLUMN updated_at;
ALTER TABLE oauth_code  DROP COLUMN updated_at;
ALTER TABLE oauth_grant DROP COLUMN updated_at;
ALTER TABLE api_token   DROP COLUMN updated_at;
ALTER TABLE share_grant DROP COLUMN updated_at;
ALTER TABLE membership  DROP COLUMN updated_at;
ALTER TABLE login_state DROP COLUMN updated_at;
ALTER TABLE session     DROP COLUMN updated_at;
