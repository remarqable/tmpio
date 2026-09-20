-- +goose Up
-- The local credential is identified by a username, which may be an email
-- address but usually is not: a private instance has one account and nothing
-- to send mail to, so "admin" is the sensible default. The column was named
-- for what the first implementation happened to store.
ALTER TABLE local_credential RENAME COLUMN email TO username;

-- +goose Down
ALTER TABLE local_credential RENAME COLUMN username TO email;
