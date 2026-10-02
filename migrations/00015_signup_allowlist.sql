-- +goose Up
-- Who may create an account while sign-ups are closed (SIGNUPS_ENABLED=0).
-- An address on this list gets their own site on first sign-in, exactly as if
-- sign-ups were open; everyone else is refused as before. It matches the
-- address the identity provider has verified, never one the visitor typed.
--
-- This belongs to the installation, not to an organization, so it has no
-- tenant_id and no row-level security, like instance_setting. Only the
-- instance admin can change it, which the request layer enforces.
--
-- An invitation link is the other way in while sign-ups are closed; that needs
-- no row here; the invite itself is the permission.
CREATE TABLE signup_allow (
  email             TEXT PRIMARY KEY CHECK (email = lower(btrim(email)) AND email <> ''),
  note              TEXT NOT NULL DEFAULT '',
  added_by_user_id  BIGINT REFERENCES "user"(id) ON DELETE SET NULL,
  used_at           TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS signup_allow;
