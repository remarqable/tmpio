# tmp

**tmp is a publishing layer for your AI.** A person signs in with Google and gets one private, web-addressable knowledge site. They connect an AI client once over MCP. From then on they ask that client to publish or update pages. Humans read a rendered website; agents read and write ordinary Markdown through a small filesystem interface (`tmp_read`, `tmp_write`, `tmp_list`, and so on). Content is Markdown, site structure is one `tmp.yaml`, and presentation is supplied by tmp.

The site is always private. The owner can mint secret links that grant read and write access to exactly one document, without an account or an MCP connection. Every change is an immutable revision. Writes use compare-and-swap on the revision number, so a stale write from any client fails instead of overwriting someone else's edit. tmp does not research, generate text, or run a model; the connected AI does that.

tmp is open source under the [MIT license](LICENSE). Run it yourself with Docker or as one Go binary beside PostgreSQL (see Run it, [docs/DOCKER.md](docs/DOCKER.md) and [docs/SETUP.md](docs/SETUP.md)), or use the hosted service at [tmp.io](https://tmp.io) when it opens. Security reports: [SECURITY.md](SECURITY.md). 

## Project configuration

This application follows the blueprint in `blueprint/claude.md` (a read-only git submodule). Its configuration block:

```yaml
tenancy:   shared     # every signup creates an organization; all content tables carry tenant_id under RLS
database:  postgres   # RLS and full-text search are required; SQLite has neither
realtime:  false      # a write is visible on the next request; no push channel is needed
jobs:      false      # rendering, search updates and image storage are local transactional work
ai:        false      # the user's connected AI supplies content; tmp calls no model
frontend:  server     # html/template pages with HTMX; no SPA or npm build
deploy:    binary     # one Go binary with embedded templates and static files, run under systemd
```

## Repository layout

```
cmd/api/                    the single server binary
internal/controllers/       browser, REST, secret-link and OAuth adapters; router.go lists every route
internal/mcp/               MCP server (Streamable HTTP) exposing the ten tmp_* tools
internal/middleware/        request ID, logging, security headers, session, bearer, CSRF, rate limits
internal/models/            domain logic: paths, filesystem operations, share grants, credentials, tmp.yaml, export
internal/platform/          config, db (RLS scoping), errors, auth (Google OIDC), render (Markdown), i18n, obs, ratelimit
internal/assets/            embedded templates (views/) and static files (static/)
migrations/                 goose SQL, 00001_platform through 00012 (also embedded in the binary)
scripts/                    e2e.sh (10-step core proof), loadcheck.sh (bounded load check), screenshots.mjs (visual checks)
install.sh                  one-command installer for a fresh Debian or Ubuntu server
config/local.env.example    every environment variable with a development default
blueprint/                  architecture blueprint (submodule, do not edit)
docs/                       DOCKER, SETUP, RUNBOOK, FORMAT, API, MCP, SHARING, SECURITY
```

## Run it

On a fresh Debian or Ubuntu server with a domain pointed at it, one command
installs Docker and Caddy, generates the secrets, gets a certificate and starts
the stack:

```bash
curl -fsSL https://raw.githubusercontent.com/remarqable/tmpio/main/install.sh \
  | sudo bash -s -- --domain tmp.example.com
```

It asks you to set a password for the `admin` account, or generates one and
prints it if it cannot ask. [install.sh](install.sh) is
about 290 lines and worth reading before you pipe it to root; it writes three
files and hands off to `docker compose`, and it does not phone home.

Prefer to do it yourself, or already have a reverse proxy? Two containers and
one `.env`; the server sets up its own database role, schema and owner account:

```bash
mkdir tmp && cd tmp
curl -O https://raw.githubusercontent.com/remarqable/tmpio/main/docker-compose.yml
curl -o .env https://raw.githubusercontent.com/remarqable/tmpio/main/config/docker.env.example
$EDITOR .env      # APP_ORIGIN, SESSION_SECRET, the two database passwords, OWNER_PASSWORD
docker compose up -d
```

Sign in with the address and password you set.

### Updating

The installer puts a `tmp` command on the host:

```bash
tmp update     # pull the current image and restart onto it
tmp status     # running, healthy, and which version
tmp logs       # follow the server log
```

`tmp update` prints the version before and after, so you can tell a real
update from a no-op, and it waits for the container's health check rather
than returning as soon as Docker accepts the request. If it does not come up
healthy it prints the log and exits non-zero.

It is a wrapper around `docker compose` in the install directory. By hand,
the same thing is `cd /opt/tmp && docker compose pull && docker compose up
-d`; the new container migrates itself. Full instructions, including backups,
your own PostgreSQL and building the image from source, are in
[docs/DOCKER.md](docs/DOCKER.md).

## Quick start (from source)

Prerequisites: a Go toolchain (go.mod says `go 1.26.0`; newer Go downloads it), PostgreSQL 16 client and server binaries (Homebrew `postgresql@16`), and `goose`.

```bash
go install github.com/pressly/goose/v3/cmd/goose@latest

cp config/local.env.example config/local.env
# edit config/local.env: set SESSION_SECRET to at least 32 random characters
#   openssl rand -base64 48

make db-init      # creates a project-local PostgreSQL 16 cluster in data/pg16 on port 5433,
                  # roles app_owner and app_user (NOBYPASSRLS), databases tmp and tmp_test
make migrate      # applies migrations as app_owner to tmp and tmp_test
make run          # starts the server on http://localhost:8000
```

Open http://localhost:8000. With `DEV_LOGIN_BYPASS=1` (the example default) the landing page shows a development sign-in form that accepts any email address. Signing in creates your user, organization, owner membership, `/`, `/index.md` and `/tmp.yaml` once. Repeat sign-ins create nothing.

`make db-start` and `make db-stop` control the local cluster later. `make test` runs the full suite against `tmp_test`; `make check` runs the blueprint boundary grep. `scripts/e2e.sh` demonstrates the core proof against the running server (requires the `local-test` client in `OAUTH_CLIENTS_JSON`).

## The core proof flow

| Step | Action | Where |
|---|---|---|
| 1 | Sign in without naming anything | `/` then Continue with Google, or the dev form (`POST /auth/dev`) |
| 2 | Connect an AI client | `/admin/connections` shows the MCP URL; client performs OAuth at `/oauth/authorize`; or create a REST token there |
| 3 | Create `/research/circle.md` through the client | MCP `tmp_write` with `expected_revision: 0`, or `PUT /api/v1/orgs/{org}/entries?path=/research/circle.md` with `If-None-Match: *` |
| 4 | Create a writable secret link | `/app/share?path=/research/circle.md` then Create edit link, or `POST /api/v1/orgs/{org}/share-links` (owner session) |
| 5 | Edit from a signed-out browser | open `/s/{token}/edit`, change text, Save |
| 6 | Edit over HTTP with no account | `GET /s/{token}/raw` (keep the ETag) then `PUT /s/{token}/raw` with `If-Match` and `Idempotency-Key` |
| 7 | Read the update through the owner connection | MCP `tmp_read`, or `GET /api/v1/orgs/{org}/entries?path=/research/circle.md` |
| 8 | Show a stale write being rejected | repeat step 6 with the old ETag: `412 revision_conflict`; MCP `tmp_write` with an old `expected_revision` returns `revision_conflict` |
| 9 | Restore a prior revision, then move the page | `/app/history?path=...` then Restore; `POST /api/v1/orgs/{org}/moves`; the `/s/{token}` link still works |
| 10 | Revoke the link | `/app/share` Revoke or `DELETE /api/v1/orgs/{org}/share-links/{id}`; `/s/{token}`, `/raw`, `/edit` and `/assets/*` now return 404 while owner access continues |

## Documentation

| Document | Contents |
|---|---|
| [docs/DOCKER.md](docs/DOCKER.md) | self-hosting with Docker: install, update, backup and restore, your own PostgreSQL, what the container does at startup |
| [docs/SETUP.md](docs/SETUP.md) | prerequisites, database roles, environment variables, Google and AI client registration, public HTTPS, tests, build, systemd, production checklist |
| [docs/RUNBOOK.md](docs/RUNBOOK.md) | start/stop, migrations, backup and restore, secret rotation, revocation, quotas, health, logs, incidents |
| [docs/FORMAT.md](docs/FORMAT.md) | page format, extensions, links, images, path grammar, `tmp.yaml` schema, sidebar ordering |
| [docs/API.md](docs/API.md) | authentication, address forms and representations, REST endpoints, headers, status codes, curl examples |
| [docs/MCP.md](docs/MCP.md) | endpoint, discovery, OAuth flow, the ten tools, error codes, agent workflow, client setup, compatibility record |
| [docs/SHARING.md](docs/SHARING.md) | secret-link model and HTTP contract |
| [docs/SECURITY.md](docs/SECURITY.md) | threat model and controls, data at rest, what leaves the server |
| [docs/DOCKER.md](docs/DOCKER.md) | self-hosting, updates, backups, publishing your own front page |

## Status and known limitations

This is an early build. It runs as one Go binary against PostgreSQL, in a container or under whatever supervisor you prefer, behind a reverse proxy that terminates TLS.

**The hosted service at tmp.io is not open yet.** Self-hosting works today and is the supported path for anyone whose threat model includes the operator.

**Client setup is documented but lightly exercised.** The MCP endpoint has been driven end to end against a public HTTPS origin — dynamic client registration, an OAuth grant with PKCE, and a write — but the vendor connector interfaces themselves have had little use. See the compatibility record in [docs/MCP.md](docs/MCP.md).

**Other limitations in this build**

- The rate limiter is in-process memory. It is per node and resets on restart.
- There is no operator purge command for revision history; see the runbook.
- Content is not end-to-end encrypted; see docs/SECURITY.md "Data at rest".
- Directory history and restore are not supported; empty directories are recreated instead.

## Owner UI

Signed-in owners land on their site. The left nav is the folder tree with owner tools (new page, new folder, upload, trash, format guide), and every content operation happens at a verb-prefixed URL of the page it acts on: `/edit/research/circle`, `/history/research/circle`, `/share/research/circle`, `/move/research/circle`, `/new/research/`, `/trash`. The admin area at `/admin` (Overview, Connections, Sharing links, Site settings, Export) holds account and site administration only and links back to the site. New sites default to the dark appearance. The old `/app` URLs redirect. Destructive actions confirm in a dialog, rename/move is a dialog on the page, and copy actions show a toast.

## AI-assisted filing

The AI is usually the writer, so the site helps decide where things go. `tmp_organize` (MCP), `POST /api/v1/orgs/{org}/organize` (REST) and the owner's **Quick add** dialog take a note, a table or a file and answer with a path: a snapshot of the tree (folders, page titles and tags, at most 300 lines) plus the first part of the text goes to a small model (`ANTHROPIC_API_KEY`, default `claude-haiku-4-5-20251001`), the answer is checked against the path grammar and the live tree, and nothing is written until the caller writes. The model is used only for organizations whose owner turned it on in Site settings; it is off by default and nothing leaves the server while it is off. `tmp_write` with `path: "auto"` files and writes in one step and never overwrites. Without a key, a rule-based fallback picks a folder named in the text or `/inbox`. Calls are audited without content (`ai_call`) and summarized on the admin overview. See docs/API.md "Filing".
