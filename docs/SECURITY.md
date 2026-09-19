# Security

This document summarizes the threat model of tmp and the controls in this build. File references point at the implementation so each claim can be checked.

## Threat model

Assets: private Markdown content, images, revision history, account identity, and the credentials that reach them (sessions, API tokens, OAuth tokens, share tokens). Actors: other tenants on the same database, anonymous internet clients, holders of leaked links or tokens, and content authors who paste hostile Markdown. Out of scope for this build: a compromised host or database role, a malicious operator, and denial of service beyond per-node rate limiting.

tmp is **not end-to-end encrypted**. The server reads content to render, search and serve it. Content is encrypted in transit and at rest by the hosting provider (see "Data at rest"), which protects against a lost disk or a leaked backup, not against someone with access to the running server or the database credentials. Anyone whose threat model includes the operator should self-host; the code is open source for that reason. Application-level encryption with per-tenant keys is planned and is tracked in the repository.

Vulnerability reports: see [SECURITY.md](../SECURITY.md) at the repository root for the disclosure policy and contact.

Guiding rules from the specification: URLs are identifiers, never credentials, except `/s/{token}`. Tenant isolation is enforced by the database, not by remembering to add a predicate. Bearer failures never fall back to cookies. Invalid links and unauthorized explicit org URLs are indistinguishable 404s.

## Tenant isolation

| Control | Implementation |
|---|---|
| Runtime role cannot bypass RLS | The server connects as `app_user`, created `LOGIN NOBYPASSRLS` (Makefile `db-init`). `app_owner` is used only by migrations, backups and tests. |
| Forced policies on every tenant table | `migrations/00002_content.sql`, `00003_credentials.sql`: `ENABLE` and `FORCE ROW LEVEL SECURITY` on membership, asset_blob, entry, revision, path_alias, audit_event, mutation_receipt, share_grant, share_grant_asset, api_token, oauth_grant, oauth_code, oauth_token. Each policy has both `USING` and `WITH CHECK` on `tenant_id = current_setting('app.tenant_id')`. |
| Transaction-local tenant context | `db.WithTenant` sets `app.tenant_id` with `set_config(..., true)` inside a transaction (`internal/platform/db/db.go`). No tenant means zero rows, not all rows. |
| Per-tenant advisory lock for mutations | `db.WithTenantSerialized` takes `pg_advisory_xact_lock(tenant_id)`. Every content mutation uses it, so path reservations and compare-and-swap checks cannot race within a tenant. |
| Token lookups before a tenant is known | `lookup_share_grant`, `lookup_api_token`, `lookup_oauth_token`, `lookup_oauth_code` are `SECURITY DEFINER` SQL functions with `search_path = public`. They accept a hash and return identifiers and status only, never content. |
| Composite foreign keys | `entry.parent_id`, `entry.blob_id`, `revision.entry_id`, `path_alias.entry_id`, `share_grant.entry_id`, `share_grant_asset.*`, `oauth_code.grant_id`, `oauth_token.grant_id` all reference `(tenant_id, id)`, so a row cannot point into another tenant even if a policy were wrong. |
| Owner handle kept out of request code | `make check` fails the build if `db.Unscoped()` appears in `internal/controllers`, `internal/models` or `internal/mcp`. |
| Domain-level checks on top of RLS | Share principals are additionally bound to one entry ID and checked in `Ops.Read`, `Ops.Write`, `ShareAsset`. API and OAuth principals carry a tenant ID that must match the `{org}` in REST URLs. |

Automated evidence: `internal/models/integration_test.go` runs as `app_user` against `tmp_test`. `TestEveryTenantTableHasRLS` queries `pg_class` and fails if any tenant-scoped table lacks forced RLS or a `USING` plus `WITH CHECK` policy. `TestTenantIsolation` proves a second tenant cannot read, insert, update or delete the first tenant's rows by path or by ID, that the unscoped handle returns zero rows, that a missing tenant is refused, that composite foreign keys reject a parent from another tenant, and that search is tenant-bound.

## Data at rest and in transit

| Control | Implementation |
|---|---|
| TLS to the database is mandatory in production | `config.Load` refuses `DATABASE_URL` or `DATABASE_OWNER_URL` without `sslmode=require`, `verify-ca` or `verify-full` when `APP_ENV=prod` (`internal/platform/config/config.go`, `requireTLS`). A database whose host resolves entirely to loopback or private addresses is exempt, which covers the self-hosted container stack where the two processes share a private network; a host that cannot be resolved is treated as public and refused. |
| HTTPS origin is mandatory in production | `config.Load` refuses a non-`https://` `APP_ORIGIN` in production, so Secure cookies and HSTS are always on. |
| Storage encryption | The managed PostgreSQL volume and its automated backups are encrypted by the provider (DigitalOcean). Nothing tenant-owned is written to the application host's disk: content lives in database rows and `bytea`, the binary embeds its templates, and logs carry no content. |
| Operator dumps | `pg_dump` output is piped through `age` to a recipient whose private key is held off the server (RUNBOOK "Backup"). Plaintext dumps never exist as files. |
| What is not encrypted at the application layer | Page sources, revision history, asset bytes, titles and the search index are plaintext to the database role. The database role is confined by RLS but not by cryptography. |

## Content sent to third parties

| Flow | Control |
|---|---|
| AI-assisted filing | Off per tenant by default (`tenant.ai_filing_enabled`, migration 9). `Ops.SuggestPlacement` calls the model only when the server has a key **and** the owner turned filing on in Site settings; otherwise the rule-based heuristic runs and nothing leaves the server. What is sent when on: the folder list, page titles, and the first 2,500 bytes of the new text (`placeMaxContentBytes`). The `ai_call` audit row stores purpose, model, token counts and outcome, never content. |
| Google sign-in | Only the OIDC identity claims are received; no Google refresh token is kept. |
| Everything else | The renderer never fetches external URLs and the server makes no other outbound calls. |

## Account lifecycle

| Control | Implementation |
|---|---|
| Instance model key | `instance_setting.ai_api_key`, set by the account marked `is_instance_admin` at `/admin/server`, or by `ANTHROPIC_API_KEY` in the environment, which wins and makes the page read-only. It is instance-wide because it pays for every organization's calls; whether a given organization's content may be sent is a separate per-tenant opt-in. Stored so it can be replayed; see "Tokens" below. |
| Local owner password | Self-hosted instances sign in with `OWNER_EMAIL` and `OWNER_PASSWORD` instead of an identity provider. The password is stored as argon2id (64 MiB, 3 passes, 2 lanes) with a per-credential salt, never in plaintext or as a fast hash. `POST /auth/local` is limited to 10 attempts per minute per IP, checks the origin, and answers a wrong address and a wrong password identically, having done the same work in both cases. It never creates an account: the owner is provisioned at startup. |
| Closed sign-ups | `SIGNUPS_ENABLED=0` makes `models.SignIn` refuse an unknown identity with `signups_closed` before anything is written; the browser lands on the public page with the waitlist. Existing users are unaffected. |
| Launch waitlist | `POST /waitlist`: same-origin check, 10 per minute per IP, honeypot field, email syntax check, `INSERT ... ON CONFLICT DO NOTHING` so the response never reveals whether an address was already listed. The table is platform-level (no tenant) and nothing in the application reads it. |
| Account deletion | `POST /admin/settings/delete-account`: owner session, CSRF, the organization code typed as confirmation, 5 per minute. `models.DeleteAccount` re-checks ownership, takes the tenant advisory lock, deletes the tenant and the user in one transaction; cascades remove every tenant table row, identities and sessions. The session cookie is cleared. |

## Authentication

| Control | Implementation |
|---|---|
| Google OpenID Connect | `internal/platform/auth/google.go`: authorization code, scopes `openid email profile`, random state stored hashed server-side with a 10-minute expiry and single use, nonce checked against the ID token, PKCE S256, ID token signature/issuer/audience verified by go-oidc, `email_verified` required, `prompt=select_account`. Identity is issuer plus `sub`. Google refresh tokens are not kept. |
| Sessions | 32 random bytes; only the SHA-256 hash is stored (`session.token_hash`). Lifetime 30 days. Signing in revokes the previous cookie's session (rotation). Logout revokes. |
| Cookie flags | `tmp_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, host-only (no Domain), `Secure` when `APP_ORIGIN` is `https://`. |
| Return path after login | `safeReturnPath`: must start with a single `/`, no scheme, host or userinfo, passes `RequestPath`, and is not under `/auth/`, `/oauth/token` or `/logout`. |
| Development bypass | `POST /auth/dev` exists only when `DEV_LOGIN_BYPASS=1`; startup refuses that setting with `APP_ENV=prod`; the handler also checks Origin. |
| Bearer handling | `middleware.Bearer`: a present `Authorization` header is validated and cookies are ignored; failure is 401 with no fallback. `tmpk_` tokens are REST/content credentials; `tmpa_` tokens are refused outside `/mcp`; anything else is 401. |
| Owner-only operations | Share-link management, export, API token and connection management, and OAuth consent require `Principal.IsOwnerSession()`, which is true only for a cookie session. |

## CSRF

| Surface | Control |
|---|---|
| Dashboard and REST with cookie session | `middleware.CSRF`: for non-GET/HEAD/OPTIONS requests without a bearer header, requires a valid owner session, an `Origin` (or `Referer`, or `Sec-Fetch-Site`) matching `APP_ORIGIN`, and `X-CSRF-Token` or form `csrf_token` equal to the session's random CSRF token (constant-time compare). |
| Secret-link editor | Nonce `grantID.expiry.HMAC-SHA256(SESSION_SECRET)` valid two hours, plus the same Origin check. No owner session is involved. |
| Secret-link HTTP PUT and preview | Origin check only when `Origin` is present; non-browser clients may omit it. Preconditions (`If-Match`, `Idempotency-Key`) are required for PUT. |
| OAuth consent POST | Owner session plus CSRF middleware. |
| Bearer requests | Exempt from CSRF; a bearer token is not ambient. |

## Path handling

- Request paths are decoded exactly once (`models.RequestPath`). Before decoding, `%2F`, `%5C`, `%00`, `%2E%2E` and `%25` are rejected. After decoding, control characters, DEL, backslash, Unicode format characters, `.` and `..` segments, and empty interior segments are rejected.
- Mutation paths (`models.ValidatePath`) additionally enforce lowercase `a-z0-9-_`, 128-byte segments, 512 bytes, 16 levels, allowed extensions, and the reserved root list (`s`, `app`, `auth`, `oauth`, `api`, `mcp`, `.well-known`, `static`, `healthz`, `readyz`, `metrics`, `login`, `logout`, `robots.txt`, `sitemap.xml`, `llms.txt`, `search`, `tmp.yaml`, `o:*`). Reserved routes are registered before the content catch-all, so an entry can never shadow a platform route.
- `o:` is recognized case-insensitively at the first segment only; a malformed code is 404, never a fallback to the personal namespace.
- Entry paths never become host filesystem paths. Content lives in PostgreSQL rows and `bytea`.
- The rendered namespace (`entry.rendered_path`, `path_alias.rendered_path`) is unique per tenant in the database and checked again under the tenant lock, so `/x.md` and `/x/` cannot both exist and move aliases cannot be reused.

## Content safety

| Control | Implementation |
|---|---|
| Raw HTML rejected | goldmark `HTMLBlock` and `RawHTML` nodes fail validation (`render.go`). Code blocks are exempt because they are escaped text. |
| Sanitizer allowlist | bluemonday policy in `sanitize.go`: fixed element list; `id` on headings; `href`, `title`, `rel=noopener noreferrer` on `a`; `src` (https or site-relative), `alt`, `title` on `img`; `class` matching `tmp-*`, `language-*`, task-list classes; checkbox inputs; `align` on cells; `role=note`. Everything else, including `style`, event handlers, `hx-*`, forms, iframes, scripts and `data:` URLs, is stripped. |
| URL schemes | Links: `https`, `http`, `mailto`. Images: `https` or local. Scheme detection strips whitespace and control characters first, so `java\tscript:` is caught. Everything else fails validation. |
| No server-side fetch | The renderer never retrieves external URLs. Local images resolve through database lookups only. |
| Not a template | Rendered HTML is inserted as `template.HTML` after sanitization and is never parsed as a Go template. |
| Response headers | `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin` (share routes: `no-referrer`), `Permissions-Policy: geolocation=(), microphone=(), camera=()`, `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'`, HSTS `max-age=31536000; includeSubDomains` on HTTPS origins. |
| Images | Signature sniffing for PNG, JPEG, WebP; extension must match; `image.DecodeConfig` bounds checked against `QUOTA_MAX_ASSET_PIXELS` before a full decode; served with `nosniff` and `Content-Disposition: inline`. No SVG. |
| Cache | All tenant and capability content is `Cache-Control: no-store`. Only `/static` is cacheable. |

## Credentials

| Credential | Prefix | Random bytes | Stored as | Audience / binding | Lifetime | Revocation |
|---|---|---|---|---|---|---|
| Browser session | none | 32 | SHA-256 | user | 30 days | logout, sign-in rotation, operator SQL |
| REST API token | `tmpk_` | 32 | SHA-256 | tenant, user, scopes | 30 days fixed | Connections page |
| OAuth authorization code | none | 32 | SHA-256 | client, redirect URI, resource, PKCE challenge, grant | 5 minutes, single use | replay revokes the grant |
| OAuth access token | `tmpa_` | 32 | SHA-256 | `APP_ORIGIN/mcp`, grant, family | 15 minutes | connection revoke, family revoke |
| OAuth refresh token | `tmpr_` | 32 | SHA-256 | `APP_ORIGIN/mcp`, client, grant, family | 30 days | rotated on use; reuse of a rotated token revokes the family; `/oauth/revoke` |
| Share token | none | 32 | SHA-256 | tenant, entry | none | Share page or API; document deletion |
| Sign-in state | none | 32 | SHA-256 | nonce, PKCE verifier, return path | 10 minutes, single use | |
| Org code | | 8 Crockford Base32 chars | plaintext | | immutable | identifier, not a secret |

All tokens come from `crypto/rand` (`models.NewToken`). Plaintext appears once, in the response that creates it, and is never logged, exported or placed in audit rows.

One credential is the exception, and it is an inbound/outbound distinction rather than an oversight: the model API key (`instance_setting.ai_api_key`) is a credential tmp *presents* to Anthropic, not one it verifies, so it is stored in a form the server can replay. A database dump therefore contains a usable provider key. The consequences: encrypt dumps (RUNBOOK "Backup"), and treat a database compromise as a compromise of that key — rotate it at the provider. Everything tmp issues for its own verification, including the local owner password, is stored only as a hash. PKCE challenges are compared after SHA-256 of the verifier; client secrets are compared in constant time.

## Rate limits

Fixed one-minute windows in process memory (`internal/platform/ratelimit`). A key's window opens on its first event, so a caller can spend a full limit at the end of one window and another at the start of the next; size limits with that in mind. Keys are principal (`user:N`, `api_token:N`, `oauth_grant:N`, `share:N`) or `ip:<addr>`.

| Routes | Limit per minute | Key |
|---|---|---|
| `/auth/google`, `/auth/google/callback`, `/auth/dev`, `/oauth/token`, `/oauth/revoke` | 30 | IP |
| REST mutations; dashboard `save`, `mkdir`, `move`, `delete`, `upload`, `restore` | 60 | principal or IP |
| REST reads; content catch-all (HTML, raw, JSON, assets, search) | 300 | principal or IP |
| `/app/export.zip`, `/api/v1/orgs/{org}/export` | 5 | principal |
| `/s/{token}` reads | 120 | IP, and 120 per grant |
| `/s/{token}` writes and preview | 30 | IP, and 30 per grant |
| `/mcp` | 300 | IP (body limited to 2 MiB) |

Exceeding a limit returns 429 with `Retry-After`. Body size: global `http.MaxBytesReader` at `QUOTA_MAX_ASSET_BYTES` + 1 MiB; pages and config are limited separately by quota. Server timeouts: 10 s read header, 60 s read, 120 s write.

## Logging and redaction

- One structured line per request with method, route template, status, duration, `request_id`, and `tenant_id`/`user_id` when known. Query strings and bodies are not logged.
- Share routes are logged as `/s/:token`, `/s/:token/raw`, and so on. The token is never written. The content catch-all logs `route=content`.
- Tokens, codes, cookies, page bodies and model output never reach the log. The MCP mutation event logs the page path and revision only.
- Metric labels are route templates, methods and status classes. No tenant, user or entry identifiers.
- Error responses carry a `request_id`; 5xx messages are replaced with `internal error`.
- `X-Forwarded-For` is believed only from `TRUSTED_PROXIES` (`r.SetTrustedProxies`, default loopback). Gin trusts every proxy unless told otherwise, which would let any caller forge the address behind every per-IP rate limit and the `/metrics` loopback gate. Regression test: `internal/controllers/http_local_auth_test.go:TestForwardedForCannotForgeLoopback`.
- Reverse proxies must not log `/s/` request paths, or must redact them. The application cannot enforce this. The shipped `scripts/deploy/Caddyfile.tmp.io` deletes `request>uri`, `Authorization` and `Cookie` from its JSON log for this reason; any other proxy in front needs the same treatment.
- A newly minted sharing link and a newly minted API token travel once through a redirect query string (`?new=`, `?token=`) to the page that displays them. They are never stored in plaintext, but they do reach the browser history and would reach any proxy log that records query strings.

## Schema changes at startup

With `AUTO_MIGRATE=1` the server creates its own runtime role and applies
migrations before it starts serving, so that a container update is one command.
Three properties make that safe to leave on:

- The role it creates is the one named in `DATABASE_URL`, always `NOBYPASSRLS`.
  The server cannot grant itself a role that escapes row-level security.
- Migrations run under a PostgreSQL advisory lock on a dedicated connection, so
  two instances starting together cannot apply the same migration twice.
- The owner connection is opened for the migration and closed again. The
  running server holds only the restricted handle.

The names and passwords that reach DDL are quoted by PostgreSQL itself through
`format('%I', ...)` and `format('%L', ...)`, never by string concatenation in
Go. tmp.io runs with `AUTO_MIGRATE` off; migrations there are an operator step.

## Not covered by this build

- No web application firewall, bot detection or CAPTCHA.
- No application-level encryption of content; see "Data at rest". Retention of revision history is an operator SQL procedure (RUNBOOK).
- The rate limiter is single-node and in memory. Multiple instances multiply the effective limits, and a restart clears the windows.
- No CDN or shared cache; all tenant content is served directly with `no-store`.
- `/mcp` is rate limited per IP, not per grant.
- Search snippets and titles are derived from content the tenant wrote; a hostile page cannot affect another tenant, but can contain misleading text for its own readers.
