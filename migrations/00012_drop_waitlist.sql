-- +goose Up
-- The launch waitlist was one company's launch campaign compiled into every
-- installation: a route, a table and a landing section that a self-hosted
-- operator has no use for. Marketing now lives outside the application.
DROP TABLE IF EXISTS launch_signup;

-- +goose Down
CREATE TABLE launch_signup (
  id         BIGSERIAL PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  source     TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
