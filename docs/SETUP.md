# Setup

This document covers a local development install and a single-node production install of tmp.

## Prerequisites

| Requirement | Notes |
|---|---|
| Go toolchain | `go.mod` declares `go 1.26.0`. Any recent Go downloads that toolchain automatically on first build. |
| PostgreSQL 16 | Homebrew: `brew install postgresql@16`. The formula is keg-only; binaries live in `/opt/homebrew/opt/postgresql@16/bin`. The Makefile uses that path through `PG_BIN`. Docker is an alternative (see below). |
| goose | `go install github.com/pressly/goose/v3/cmd/goose@latest` |
| make | Ships with macOS developer tools and every Linux distribution. |

Only one binary is built: `cmd/api`. Templates, static files and language catalogs are embedded.

## Database roles and databases

tmp requires two PostgreSQL roles. This is a blueprint rule, not a convenience.

| Role | Attributes | Used by |
|---|---|---|
| `app_owner` | owns the tables and functions | `make migrate`, backups, operator SQL |
| `app_user` | `LOGIN NOBYPASSRLS`, no ownership | the running application (`DATABASE_URL`) |

Table owners bypass row-level security. If the application connected as `app_owner`, every query would see every tenant. The RLS policies in `migrations/00002_content.sql` and `00003_credentials.sql` use `FORCE ROW LEVEL SECURITY` with both `USING` and `WITH CHECK`. Token lookups by hash run through `SECURITY DEFINER` functions owned by `app_owner` and return identifiers only.

Two databases exist: `tmp` (development data) and `tmp_test` (truncated by tests).

### Option A: project-local cluster (macOS, Homebrew)

```bash
make db-init
```

This runs `initdb` into `data/pg16` with superuser `app_owner`, configures port `5433` and loopback listening, starts the server, sets the `app_owner` password to `app`, creates `app_user` with `NOBYPASSRLS`, creates `tmp` and `tmp_test`, and grants default privileges so `app_user` can use tables, sequences and functions that `app_owner` creates later. Logs go to `data/pg16.log`. `data/` is ignored by git.

```bash
make db-start   # after a reboot
make db-stop
```

### Option B: Docker

```bash
docker run --name tmp-db -e POSTGRES_USER=app_owner -e POSTGRES_PASSWORD=app \
  -e POSTGRES_DB=tmp -p 5433:5432 -d postgres:16-alpine
docker exec -i tmp-db psql -U app_owner -d postgres <<'SQL'
CREATE ROLE app_user LOGIN PASSWORD 'app' NOBYPASSRLS;
CREATE DATABASE tmp_test OWNER app_owner;
SQL
for d in tmp tmp_test; do docker exec -i tmp-db psql -U app_owner -d $d <<'SQL'
GRANT USAGE ON SCHEMA public TO app_user;
ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user;
ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT USAGE ON SEQUENCES TO app_user;
ALTER DEFAULT PRIVILEGES FOR ROLE app_owner IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO app_user;
SQL
done
```

The example env file already points at `127.0.0.1:5433`.

## Environment variables

Copy `config/local.env.example` to `config/local.env`. The Makefile sources this file for `run`, `test`, `migrate` and related targets. Startup fails with a clear message when a required value is missing.

| Variable | Default (example file) | Meaning |
|---|---|---|
| `APP_ENV` | `dev` | `prod` or `production` enables production checks (see checklist). Also switches the logger from console to JSON. |
| `PORT` | `8000` | Listen port. The server binds `0.0.0.0:PORT`. |
| `APP_ORIGIN` | `http://localhost:8000` | Public origin without trailing slash. Used for absolute URLs, the OAuth issuer, the MCP resource identifier (`APP_ORIGIN/mcp`), Origin checks and Host checks. An `https://` origin turns on Secure cookies and HSTS. |
| `DATABASE_URL` | `postgres://app_user:app@127.0.0.1:5433/tmp?sslmode=disable` | Runtime connection as the RLS-enforced role. Required. |
| `DATABASE_OWNER_URL` | `postgres://app_owner:app@127.0.0.1:5433/tmp?sslmode=disable` | Owner connection for `make migrate` and backups. The server does not open it. |
| `TEST_DATABASE_URL` | `...5433/tmp_test...` as `app_user` | Runtime role against the test database. Tests skip when unset. |
| `TEST_DATABASE_OWNER_URL` | `...5433/tmp_test...` as `app_owner` | Owner role for the test database (migrations, truncation). |
| `SESSION_SECRET` | `change-me-to-a-long-random-value` | At least 32 characters. HMAC key for secret-link editor CSRF nonces and pagination cursors. Change it before first use. |
| `GOOGLE_CLIENT_ID` | empty | Google OAuth client ID. Empty disables Google sign-in. |
| `GOOGLE_CLIENT_SECRET` | empty | Google OAuth client secret. |
| `GOOGLE_REDIRECT_URL` | `http://localhost:8000/auth/google/callback` | Must equal `APP_ORIGIN + /auth/google/callback` and the URI registered at Google. Defaults to that value when empty. |
| `DEV_LOGIN_BYPASS` | `1` | `1` or `true` enables the development sign-in form (`POST /auth/dev`). Startup fails if set with `APP_ENV=prod`. |
| `OAUTH_CLIENTS_JSON` | unset | JSON array of preregistered OAuth clients that adds to or replaces the built-in `claude` and `chatgpt` entries. |
| `METRICS_TOKEN` | empty | When set, `GET /metrics` requires `Authorization: Bearer <token>`. When empty, `/metrics` answers loopback clients only. |
| `OWNER_USER` | `admin` when `OWNER_PASSWORD` is set | The one account that may sign in with a password. A username, not necessarily an address: a private instance has one account and nothing to send mail to. An email address works if you prefer one. Created at startup with its organization and starting pages, exactly as a first sign-in would. `OWNER_EMAIL` is the old name and still works, so an instance configured with it keeps its account. |
| `OWNER_PASSWORD` | empty | The password for that account, at least 12 characters. Setting it alone provisions `admin`. Authoritative at every boot: a new value rotates the password, which is also how a forgotten one is recovered. Once the account exists this may be removed. |
| `LOCAL_AUTH` | on when an owner is configured | Explicitly turns the password sign-in form on or off. `0` hides it even when an owner account exists. |
| `PUBLIC_SITE_DIR` | empty | A directory of static files served to visitors who are not signed in, ahead of the built-in sign-in page. Reserved paths (`/login`, `/s/`, `/mcp`, `/api`, `/admin`, `/.well-known`) are never shadowed, and a signed-in visitor always gets the application. `robots.txt`, `sitemap.xml`, `favicon.ico` and `llms.txt` are served from here when present. Pretty URLs resolve: `/about` finds `about.html` or `about/index.html`. |
| `PUBLIC_SITE_CSP` | empty | Replaces the `Content-Security-Policy` for those files only. Empty keeps the application's strict policy. Files here execute on the application's origin, so script in them can act as a signed-in visitor: treat write access to the directory as deploy access to the application. |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Comma-separated IP addresses or CIDR blocks whose `X-Forwarded-For` is believed. Every per-IP decision — rate limit keys, the `/metrics` loopback gate, the client registration quota — reads the resulting address, so an address trusted without being in front of the server lets any caller claim to be anyone. The default suits a reverse proxy on the same host; in Docker the proxy appears as the bridge gateway, so the compose file sets `172.16.0.0/12`. The value `none` ignores the header and uses the peer address. |
| `AUTO_MIGRATE` | `0` | `1` makes the server create its runtime database role, apply pending migrations as the owner role and provision the owner account at startup. The container image sets it; a deployment where an operator runs `make migrate` should not. |
| `SIGNUPS_ENABLED` | `1` | `0` closes new-account creation: a first sign-in is refused and the landing page offers the launch waitlist (`POST /waitlist`, table `launch_signup`). Existing accounts sign in as usual. |
| `SOURCE_URL` | `https://github.com/remarqable/tmpio` | Public repository linked from the landing page and footer. |
| `ANTHROPIC_API_KEY` | empty | Makes AI-assisted filing available (Quick add, `tmp_organize`, `tmp_write` path `auto`, `POST /organize`). Empty means rule-based filing only. Even with a key, nothing is sent for an organization until its owner turns filing on in Site settings; then the folder list, page titles and the first 2,500 bytes of the new text go to Anthropic and nothing else leaves the server. |
| `AI_WORKSPACE_ID` | empty | Anthropic workspace ID (`wrkspc_...`). Required when the key is an organization-level key not scoped to one workspace; Anthropic rejects such calls with `must include the anthropic-workspace-id header`. A key created inside a workspace does not need it. |
| `AI_MODEL` | `claude-haiku-4-5-20251001` | Model used for filing. A small fast model is enough: the job is one classification per note. |
| `AI_BASE_URL` | `https://api.anthropic.com` | Override for a proxy or a test double. |
| `AI_TIMEOUT_SECONDS` | `8` | Per-call timeout; on timeout the heuristic answers. |
| `AI_MAX_CALLS_PER_HOUR` | `120` | Per-tenant budget of model calls; beyond it the heuristic answers until the hour passes. |
| `QUOTA_MAX_PAGES` | `1000` | Live pages per tenant. |
| `QUOTA_MAX_DIRECTORIES` | `100` | Live directories per tenant, excluding root. |
| `QUOTA_MAX_PAGE_BYTES` | `262144` | Maximum Markdown source per page (256 KiB). |
| `QUOTA_MAX_CONFIG_BYTES` | `32768` | Maximum `tmp.yaml` size (32 KiB). |
| `QUOTA_MAX_ASSET_BYTES` | `5242880` | Maximum image upload (5 MiB). The global request body limit is this value plus 1 MiB. |
| `QUOTA_MAX_ASSET_PIXELS` | `20000000` | Maximum decoded width times height. |
| `QUOTA_MAX_CURRENT_ASSET_BYTES` | `104857600` | Sum of live asset bytes per tenant (100 MiB). |
| `QUOTA_MAX_RETAINED_BYTES` | `524288000` | Sum of all revision sources plus all asset blobs per tenant (500 MiB). History is never discarded automatically. |

Startup requires at least one sign-in method: Google credentials or `DEV_LOGIN_BYPASS=1`.

## Google OAuth client

1. Open the Google Cloud Console and create or select a project.
2. APIs and Services, then OAuth consent screen. Choose External (or Internal for a Workspace-only pilot). Add the scopes `openid`, `email` and `profile`. Add your test users while the app is in testing.
3. Credentials, then Create credentials, then OAuth client ID, application type Web application.
4. Authorized redirect URI: exactly `APP_ORIGIN + /auth/google/callback`, for example `https://tmp.example.com/auth/google/callback`. Google compares the full string, including scheme and port.
5. Copy the client ID and secret into `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`. Set `GOOGLE_REDIRECT_URL` to the same URI or leave it empty.

tmp uses the authorization-code flow with a server-stored state, a nonce, and PKCE (S256). It validates the ID token signature, issuer, audience and nonce. It requires `email_verified: true` and identifies users by issuer plus `sub`, never by email alone. Google refresh tokens are discarded.

## Registering AI clients

Preregistered clients are synced into the `oauth_client` table at startup. Clients can also register themselves through `POST /oauth/register` (RFC 7591, public clients only; see docs/MCP.md), which is what Claude Code and other MCP clients do automatically.

Built-in clients (`internal/platform/config/config.go`):

| Client ID | Name | Redirect URIs | Type |
|---|---|---|---|
| `claude` | Claude | `https://claude.ai/api/mcp/auth_callback`, `https://claude.com/api/mcp/auth_callback` | public (PKCE only) |
| `chatgpt` | ChatGPT | `https://chatgpt.com/connector_platform_oauth_redirect`, `https://chat.openai.com/connector_platform_oauth_redirect` | public (PKCE only) |

`OAUTH_CLIENTS_JSON` adds clients or replaces a built-in one with the same `id`. Each entry needs `id` and `redirect_uris`. `"public": true` means no client secret. A `"secret"` field makes the client confidential; only its SHA-256 hash is stored.

The value contains spaces and quotes, so it must be single-quoted in the env file:

```bash
OAUTH_CLIENTS_JSON='[{"id":"local-test","name":"Local test client","redirect_uris":["http://127.0.0.1:9876/cb"],"public":true}]'
```

Redirect URIs are matched exactly at `/oauth/authorize`. A request with an unknown client or unregistered redirect URI gets a 400 page and is never redirected.

## Exposing a public HTTPS origin

Claude and ChatGPT connect from the vendor's cloud. A localhost URL cannot be reached. For testing, start a tunnel and point `APP_ORIGIN` at it.

```bash
# ngrok
ngrok http 8000
# cloudflared
cloudflared tunnel --url http://localhost:8000
```

Then in `config/local.env`:

```bash
APP_ORIGIN=https://abc123.ngrok-free.app
GOOGLE_REDIRECT_URL=https://abc123.ngrok-free.app/auth/google/callback
```

Add the same redirect URI at Google. Restart the server after changing `APP_ORIGIN`: the OAuth issuer, the MCP resource identifier and the Host check all derive from it. Tunnels change hostname on restart unless you reserve one.

## Public HTTPS with `make tunnel`

MCP clients refuse `http://` sign-in URLs: Claude Code reports "Refused to open sign-in URL: must be https". `make tunnel` starts an authenticated ngrok tunnel to port 8000, rewrites `APP_ORIGIN` in `config/local.env` to the tunnel URL, rebuilds and restarts the server, and prints the MCP URL to give clients. `make tunnel-stop` reverses it. The free ngrok URL changes on every start, so re-add the client afterwards, for example `claude mcp add --transport http tmpio https://<id>.ngrok.app/mcp`. While the tunnel is up the development sign-in bypass is reachable from the internet at that random URL; stop the tunnel when you finish testing, or configure Google sign-in and unset `DEV_LOGIN_BYPASS`.

## Running

```bash
make run
```

`make run` exports `config/local.env` and runs `go run ./cmd/api`. Set `ENV=path/to/other.env` to use a different file. Set `TEMPLATE_DEV=1` in the environment to reload templates from disk during development.

## Tests

```bash
make test        # go test ./... -race -count=1 -p 1 with config/local.env exported
make test-unit   # renderer, path grammar, tmp.yaml, principal and token tests; no database
```

PostgreSQL-backed tests read `TEST_DATABASE_URL` and `TEST_DATABASE_OWNER_URL` and skip when they are unset. The helper `internal/platform/db/testdb.go` connects the runtime handle as `app_user` and truncates every table before a test through the owner handle. All packages share the one `tmp_test` database, which is why `make test` passes `-p 1` (one package at a time).

| Package | Files | Covers |
|---|---|---|
| `internal/platform/render` | `frontmatter_test.go`, `extensions_test.go`, `links_test.go`, `render_test.go`, `rewrite_test.go` | frontmatter limits and errors, the three extensions, link and image rules, sanitizer, heading IDs and TOC, relative-link rewriting on move |
| `internal/models` | `paths_test.go`, `siteconfig_test.go` | path grammar, request-path decoding, `o:` prefix, cursors, scopes, `tmp.yaml` schema |
| `internal/models` | `integration_test.go` (PostgreSQL, runtime role) | RLS coverage query over every tenant table, four-direction isolation including composite FK rejection and shared-handle fail-closed, signup exactly once, compare-and-swap, idempotency and unchanged writes, six concurrent writers with one winner, directories, collisions and pagination, move with alias and relative-link rewrite, delete revokes share links and restore does not reactivate them, share capability boundaries, embedded-asset allowlist, search, quotas, export snapshot, OAuth code replay, refresh reuse and revocation, API tokens, restore rejecting content invalid under the current parser |
| `internal/controllers` | `http_test.go`, `http_share_test.go`, `testharness_test.go` | both address forms and authorization (A15), CSRF and dashboard forms, the REST contract, the secret-link HTTP contract |
| `internal/mcp` | `mcp_test.go` | SDK client over Streamable HTTP: 401 challenge, wrong-audience refusal, `tools/list` annotations, all ten tools, structured errors, live-session revocation |

Scripts against a running dev server: `scripts/e2e.sh [origin]` runs the ten-step core proof and needs the `local-test` client from the `OAUTH_CLIENTS_JSON` example; `scripts/loadcheck.sh [origin] [pages]` seeds pages and measures latency; `scripts/screenshots.mjs` captures headless Chrome screenshots and needs Google Chrome on macOS and Node 22 or newer.

`make migrate-down` and `make db-reset` act on the test database only.

## Building the binary

```bash
make build            # writes bin/api
go vet ./... && make check
```

For Linux deployment from macOS:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/api-linux ./cmd/api
```

The binary embeds templates, static files and language catalogs. It needs only the environment file and a reachable PostgreSQL.

## systemd unit (deploy: binary)

```ini
# /etc/systemd/system/tmp.service
[Unit]
Description=tmp publishing layer
After=network.target postgresql.service

[Service]
Type=simple
User=tmp
Group=tmp
WorkingDirectory=/opt/tmp
EnvironmentFile=/opt/tmp/config/prod.env
ExecStart=/opt/tmp/bin/api
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo install -d -o tmp -g tmp /opt/tmp/bin /opt/tmp/config
sudo install -m 0640 -o root -g tmp config/prod.env /opt/tmp/config/prod.env
sudo systemctl enable --now tmp
sudo journalctl -u tmp -f
```

Run migrations once per deploy, before restarting the service:

```bash
goose -dir migrations postgres "$DATABASE_OWNER_URL" up
```

Put a reverse proxy (Caddy or Nginx) in front for TLS. Forward `Host` unchanged; the MCP handler rejects requests whose `Host` differs from `APP_ORIGIN`. Set `X-Forwarded-For` so rate limits key on the client address.

## Production checklist

- `APP_ENV=prod`. Startup then fails unless a sign-in method exists — `GOOGLE_CLIENT_ID` with `GOOGLE_CLIENT_SECRET`, or `OWNER_PASSWORD` for a single local owner — and fails if `DEV_LOGIN_BYPASS` is set.
- `APP_ORIGIN` is an `https://` URL. Session cookies get the `Secure` flag and HSTS is sent only when it is.
- `SESSION_SECRET` is at least 32 random characters and is not the example value.
- `DATABASE_URL` connects as `app_user` (`NOBYPASSRLS`), not `app_owner`. Both database URLs must carry `sslmode=require`, `verify-ca` or `verify-full` when the database is at a public address; startup refuses `disable`, `prefer`, `allow` or a missing mode in production. Prefer `verify-full` with the provider's CA certificate. A database on a loopback or private address is exempt, because those bytes never reach a public network; a host that does not resolve is treated as public and refused.
- Storage encryption is on for the managed database and its automated backups (DigitalOcean managed PostgreSQL encrypts volumes and backups by default; confirm in the control panel). Operator dumps are encrypted with `age` before they touch disk (see RUNBOOK).
- `SIGNUPS_ENABLED=0` until the hosted service opens; the landing page then collects waitlist addresses instead of creating accounts.
- `DATABASE_OWNER_URL` is present only where migrations and backups run.
- `METRICS_TOKEN` is set if `/metrics` must be reachable from a non-loopback scraper.
- Google redirect URI matches `APP_ORIGIN/auth/google/callback`.
- Daily `pg_dump` with a tested restore (see RUNBOOK).
- `make check` passes: no `db.Unscoped()` in request-handling code.
