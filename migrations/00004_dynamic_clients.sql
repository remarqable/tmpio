-- +goose Up
-- Dynamic client registration (RFC 7591): real MCP clients register themselves.
ALTER TABLE oauth_client ADD COLUMN is_dynamic BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE oauth_client ADD COLUMN client_uri TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_client ADD COLUMN registered_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_oauth_client_dynamic_created ON oauth_client (is_dynamic, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_oauth_client_dynamic_created;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS registered_ip;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS client_uri;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS is_dynamic;
