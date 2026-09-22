-- +goose Up
-- Several people per organization. The tenant and membership tables already
-- existed and every content policy is keyed on tenant_id, so isolation between
-- organizations is unchanged; what is new is a second member inside one, and a
-- role that decides what they may do.

-- Roles map onto the scopes the request layer already enforces:
--   owner   read, write, delete, and the organization itself (members, settings)
--   editor  read, write, delete
--   viewer  read
ALTER TABLE membership DROP CONSTRAINT IF EXISTS membership_role_check;
ALTER TABLE membership ADD CONSTRAINT membership_role_check
  CHECK (role IN ('owner', 'editor', 'viewer'));

-- Which organization a session is acting in. Deliberately not called
-- tenant_id: every table with that column is tenant-owned data under RLS, and
-- this is a preference on a row that is looked up by token before any tenant
-- is known. There is an invariant test that checks exactly this.
ALTER TABLE session ADD COLUMN IF NOT EXISTS acting_tenant_id BIGINT REFERENCES tenant(id) ON DELETE SET NULL;

-- An invitation is a secret link, like a sharing link: the token is never
-- stored, only its hash. An address is recorded so the invite can be shown in
-- the members list, but it is not proof of anything - whoever holds the link
-- accepts it, and the account they sign in with becomes the member.
CREATE TABLE invite (
  id                 BIGSERIAL PRIMARY KEY,
  tenant_id          BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  email              TEXT NOT NULL,
  role               TEXT NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
  token_hash         BYTEA NOT NULL UNIQUE,
  created_by_user_id BIGINT REFERENCES "user"(id) ON DELETE SET NULL,
  expires_at         TIMESTAMPTZ NOT NULL,
  accepted_at        TIMESTAMPTZ,
  accepted_user_id   BIGINT REFERENCES "user"(id) ON DELETE SET NULL,
  revoked_at         TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_invite_tenant ON invite (tenant_id);
ALTER TABLE invite ENABLE ROW LEVEL SECURITY;
ALTER TABLE invite FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invite
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

-- Accepting happens before the accepter is a member, so the row cannot be read
-- under the tenant policy. This is the same shape as lookup_share_grant: a
-- SECURITY DEFINER function that returns one row for one token hash and
-- nothing else, callable only from the model that owns invitations.
-- +goose StatementBegin
CREATE FUNCTION lookup_invite(p_hash BYTEA)
RETURNS TABLE (id BIGINT, tenant_id BIGINT, email TEXT, role TEXT, expires_at TIMESTAMPTZ,
               accepted_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT i.id, i.tenant_id, i.email, i.role, i.expires_at, i.accepted_at, i.revoked_at
  FROM invite i
  WHERE i.token_hash = p_hash
  LIMIT 1;
$$;
-- +goose StatementEnd

-- Accepting writes the membership and marks the invite, both outside the
-- accepter's tenant scope. Kept to one function so there is one place to audit.
-- +goose StatementBegin
CREATE FUNCTION accept_invite(p_hash BYTEA, p_user_id BIGINT)
RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
  v invite%ROWTYPE;
BEGIN
  SELECT * INTO v FROM invite WHERE token_hash = p_hash FOR UPDATE;
  IF NOT FOUND
     OR v.revoked_at IS NOT NULL
     OR v.accepted_at IS NOT NULL
     OR v.expires_at < NOW() THEN
    RETURN NULL;
  END IF;
  INSERT INTO membership (tenant_id, user_id, role)
  VALUES (v.tenant_id, p_user_id, v.role)
  ON CONFLICT (tenant_id, user_id) DO UPDATE SET role = EXCLUDED.role;
  UPDATE invite SET accepted_at = NOW(), accepted_user_id = p_user_id WHERE id = v.id;
  RETURN v.tenant_id;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION lookup_invite(BYTEA) FROM PUBLIC;
REVOKE ALL ON FUNCTION accept_invite(BYTEA, BIGINT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_invite(BYTEA) TO app_user;
GRANT EXECUTE ON FUNCTION accept_invite(BYTEA, BIGINT) TO app_user;

-- +goose Down
DROP FUNCTION IF EXISTS accept_invite(BYTEA, BIGINT);
DROP FUNCTION IF EXISTS lookup_invite(BYTEA);
DROP TABLE IF EXISTS invite;
ALTER TABLE session DROP COLUMN IF EXISTS acting_tenant_id;
ALTER TABLE membership DROP CONSTRAINT IF EXISTS membership_role_check;
ALTER TABLE membership ADD CONSTRAINT membership_role_check CHECK (role IN ('owner'));
