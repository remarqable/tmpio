-- +goose Up
-- A hidden sign-up link: anyone who opens it and signs in gets their own site,
-- even while SIGNUPS_ENABLED=0. One per installation, created, replaced and
-- turned off by the instance admin; replacing it is how an old link is
-- revoked. Empty means there is no link.
--
-- Stored as given, like the model key beside it: the only page that shows it
-- is the operator's, and a link the operator cannot read again is a link they
-- cannot pass on again.
ALTER TABLE instance_setting ADD COLUMN signup_link TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instance_setting DROP COLUMN IF EXISTS signup_link;
