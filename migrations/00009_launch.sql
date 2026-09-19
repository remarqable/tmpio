-- +goose Up
-- Launch preparation: a waitlist for the hosted service while sign-ups are closed,
-- and a per-tenant opt-in for AI-assisted filing (content excerpts leave the server
-- only when the owner has turned it on).

-- Platform table: no tenant_id, written by anonymous visitors, read by the operator.
CREATE TABLE launch_signup (
  id         BIGSERIAL PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  source     TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE tenant ADD COLUMN ai_filing_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE tenant DROP COLUMN ai_filing_enabled;
DROP TABLE IF EXISTS launch_signup;
