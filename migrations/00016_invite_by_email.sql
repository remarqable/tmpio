-- +goose Up
-- Invitations by address instead of by link. An owner adds an email and a
-- role; the next time someone signs in with that address, verified by their
-- identity provider, they join. There is no link to copy, send, forward or
-- spend by opening it in the wrong browser.
--
-- The address is now what the invite is for, so it is matched exactly
-- (lowercased) and only ever against a provider-verified address.
ALTER TABLE invite ALTER COLUMN token_hash DROP NOT NULL;
CREATE INDEX idx_invite_pending_email ON invite (email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;

DROP FUNCTION IF EXISTS accept_invite(BYTEA, BIGINT);
DROP FUNCTION IF EXISTS lookup_invite(BYTEA);

-- Accepts every pending, unexpired invitation for an address and returns the
-- organizations joined. It runs at sign-in, before the user is a member of
-- anything they are joining, so it is SECURITY DEFINER like the lookups it
-- replaces. Someone already in an organization keeps their role: an owner
-- invited again as a viewer is not demoted by signing in.
-- +goose StatementBegin
CREATE FUNCTION accept_invites_for_email(p_email TEXT, p_user_id BIGINT)
RETURNS SETOF BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
  v invite%ROWTYPE;
BEGIN
  FOR v IN
    SELECT * FROM invite
    WHERE email = lower(btrim(p_email))
      AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at >= NOW()
    ORDER BY id
    FOR UPDATE
  LOOP
    INSERT INTO membership (tenant_id, user_id, role)
    VALUES (v.tenant_id, p_user_id, v.role)
    ON CONFLICT (tenant_id, user_id) DO NOTHING;
    UPDATE invite SET accepted_at = NOW(), accepted_user_id = p_user_id WHERE id = v.id;
    RETURN NEXT v.tenant_id;
  END LOOP;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION accept_invites_for_email(TEXT, BIGINT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accept_invites_for_email(TEXT, BIGINT) TO app_user;

-- +goose Down
DROP FUNCTION IF EXISTS accept_invites_for_email(TEXT, BIGINT);
DROP INDEX IF EXISTS idx_invite_pending_email;
-- Invitations made by address have no token; they cannot be restored as links.
DELETE FROM invite WHERE token_hash IS NULL;
ALTER TABLE invite ALTER COLUMN token_hash SET NOT NULL;
