-- +goose Up
-- Tenant-scoped content tables. Every table here has tenant_id, FORCE RLS and a
-- policy with both USING and WITH CHECK. NULLIF guards the empty-string value a
-- reverted SET LOCAL leaves behind, which would otherwise fail the BIGINT cast.

CREATE TABLE membership (
  id         BIGSERIAL PRIMARY KEY,
  tenant_id  BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  user_id    BIGINT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK (role IN ('owner')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, user_id)
);
CREATE INDEX idx_membership_user ON membership (user_id);
ALTER TABLE membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership FORCE ROW LEVEL SECURITY;
-- A member may see their own memberships before a tenant is selected.
CREATE POLICY tenant_isolation ON membership
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT
         OR user_id = NULLIF(current_setting('app.user_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE asset_blob (
  id         BIGSERIAL PRIMARY KEY,
  tenant_id  BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  sha256     BYTEA NOT NULL,
  mime       TEXT NOT NULL,
  size_bytes BIGINT NOT NULL,
  width      INT NOT NULL DEFAULT 0,
  height     INT NOT NULL DEFAULT 0,
  data       BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, id)
);
ALTER TABLE asset_blob ENABLE ROW LEVEL SECURITY;
ALTER TABLE asset_blob FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON asset_blob
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE entry (
  id               BIGSERIAL PRIMARY KEY,
  tenant_id        BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  kind             TEXT NOT NULL CHECK (kind IN ('directory', 'page', 'config', 'asset')),
  path             TEXT NOT NULL,
  rendered_path    TEXT NOT NULL,
  parent_id        BIGINT,
  current_revision BIGINT NOT NULL DEFAULT 0,
  title            TEXT NOT NULL DEFAULT '',
  description      TEXT NOT NULL DEFAULT '',
  tags             TEXT[] NOT NULL DEFAULT '{}',
  order_index      INT,
  plain_text       TEXT NOT NULL DEFAULT '',
  tsv              TSVECTOR GENERATED ALWAYS AS (
                     setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
                     setweight(to_tsvector('simple', coalesce(plain_text, '')), 'B')
                   ) STORED,
  blob_id          BIGINT,
  size_bytes       BIGINT NOT NULL DEFAULT 0,
  deleted_at       TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, path),
  UNIQUE (tenant_id, rendered_path),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES entry (tenant_id, id),
  FOREIGN KEY (tenant_id, blob_id) REFERENCES asset_blob (tenant_id, id)
);
CREATE INDEX idx_entry_tenant_parent ON entry (tenant_id, parent_id, deleted_at);
CREATE INDEX idx_entry_tenant_tsv ON entry USING GIN (tsv);
CREATE TRIGGER set_entry_updated_at BEFORE UPDATE ON entry
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
ALTER TABLE entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE entry FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entry
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE revision (
  id             BIGSERIAL PRIMARY KEY,
  tenant_id      BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  entry_id       BIGINT NOT NULL,
  seq            BIGINT NOT NULL,
  operation      TEXT NOT NULL,
  path           TEXT NOT NULL,
  prior_path     TEXT,
  source         TEXT,
  blob_id        BIGINT,
  meta           JSONB NOT NULL DEFAULT '{}'::jsonb,
  actor_kind     TEXT NOT NULL,
  user_id        BIGINT REFERENCES "user"(id),
  oauth_grant_id BIGINT,
  api_token_id   BIGINT,
  share_grant_id BIGINT,
  author_name    TEXT NOT NULL DEFAULT '',
  summary        TEXT NOT NULL DEFAULT '',
  request_id     TEXT NOT NULL DEFAULT '',
  size_bytes     BIGINT NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, entry_id, seq),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES entry (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, blob_id) REFERENCES asset_blob (tenant_id, id)
);
CREATE INDEX idx_revision_tenant_created ON revision (tenant_id, created_at DESC);
ALTER TABLE revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE revision FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON revision
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE path_alias (
  id            BIGSERIAL PRIMARY KEY,
  tenant_id     BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  path          TEXT NOT NULL,
  rendered_path TEXT NOT NULL,
  entry_id      BIGINT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tenant_id, path),
  UNIQUE (tenant_id, rendered_path),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES entry (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE path_alias ENABLE ROW LEVEL SECURITY;
ALTER TABLE path_alias FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON path_alias
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE audit_event (
  id             BIGSERIAL PRIMARY KEY,
  tenant_id      BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  actor_kind     TEXT NOT NULL,
  user_id        BIGINT,
  oauth_grant_id BIGINT,
  api_token_id   BIGINT,
  share_grant_id BIGINT,
  action         TEXT NOT NULL,
  entry_id       BIGINT,
  revision_seq   BIGINT,
  request_id     TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_event_tenant_created ON audit_event (tenant_id, created_at DESC);
ALTER TABLE audit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_event FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_event
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

CREATE TABLE mutation_receipt (
  id             BIGSERIAL PRIMARY KEY,
  tenant_id      BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  principal_key  TEXT NOT NULL,
  request_id     TEXT NOT NULL,
  operation      TEXT NOT NULL,
  request_digest BYTEA NOT NULL,
  result         JSONB NOT NULL,
  share_grant_id BIGINT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at     TIMESTAMPTZ NOT NULL,
  UNIQUE (tenant_id, principal_key, request_id)
);
ALTER TABLE mutation_receipt ENABLE ROW LEVEL SECURITY;
ALTER TABLE mutation_receipt FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON mutation_receipt
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

-- +goose Down
DROP TABLE IF EXISTS mutation_receipt;
DROP TABLE IF EXISTS audit_event;
DROP TABLE IF EXISTS path_alias;
DROP TABLE IF EXISTS revision;
DROP TABLE IF EXISTS entry;
DROP TABLE IF EXISTS asset_blob;
DROP TABLE IF EXISTS membership;
