# HTTP API

tmp exposes three HTTP surfaces: content representations at both address forms, a versioned REST API under `/api/v1/orgs/{org}`, and the secret-link routes under `/s/{token}` (documented in SHARING.md). All routes are defined in `internal/controllers/router.go`. Every response from these surfaces carries `Cache-Control: no-store`.

## Text files

`PUT /entries?path=/data/report.csv` stores a text file (see docs/FORMAT.md for the extension list); the same preconditions apply. `GET /data/report.csv` on the content routes returns the raw bytes with `text/csv; charset=utf-8` to API clients and a viewer to browsers (`?raw=1` forces a download). `POST /assets?path=/docs/spec.pdf` uploads a PDF (default limit 20 MiB, `QUOTA_MAX_DOCUMENT_BYTES`).

## Authentication

| Credential | How to send | Where accepted |
|---|---|---|
| Owner browser session | cookie `tmp_session` (HttpOnly, SameSite=Lax, Secure on HTTPS). Mutations also need `X-CSRF-Token: <token>` (or form field `csrf_token`) and a same-origin `Origin` or `Referer`. The CSRF token is in the dashboard page data. | everything: dashboard, REST, content, share-link management, export |
| REST API token | `Authorization: Bearer tmpk_...`. Created once in Connections, stored hashed, valid 30 days, revocable. Scopes: `content:read`, `content:write`, `content:delete` (write and delete imply read). | REST endpoints except export and share-link management; content read routes (GET/HEAD) in both address forms |
| MCP access token | `Authorization: Bearer tmpa_...` | `/mcp` only. At REST or content routes it is refused with 401 `this token is bound to the MCP resource; use it at /mcp`. |
| Secret link token | in the URL path `/s/{token}` | share-link routes only. Never valid as a bearer credential. |

Rules:

- A present but invalid `Authorization` header is a 401. There is no fallback to cookies or anonymous access.
- When `Authorization` is present, cookies are ignored for that request.
- A bearer credential selects its bound organization. `{org}` in the URL must match or the response is 404.
- CSRF applies to cookie sessions only. Bearer requests skip it.
- 401 responses include `WWW-Authenticate: Bearer realm="tmp"`.

## Address forms and representations

Every entry has two address forms that resolve to the same entry, revision and authorization check:

```
/research/circle              personal shortcut: the authenticated principal's organization
/o:XS67DF65/research/circle   explicit: canonical private address
```

The `o:` prefix is recognized case-insensitively; generated URLs use lowercase `o:` and an uppercase eight-character Crockford Base32 code. Reserved platform routes win over content routing. An `o:` prefix with an unknown or malformed code is 404 and never falls back to the personal namespace. Content URLs accept GET and HEAD only; other methods get 405 with `Allow: GET, HEAD`.

| Want | URL (explicit form; drop `/o:{org}` for the shortcut) | Content-Type |
|---|---|---|
| Rendered page | `/o:{org}/research/circle` | `text/html` |
| Raw source of a page | `/o:{org}/research/circle.md` | `text/markdown; charset=utf-8` |
| JSON of a page | `/o:{org}/research/circle.json` | `application/json` |
| Root page rendered | `/o:{org}/` | `text/html` |
| Root source, root JSON | `/o:{org}/index.md`, `/o:{org}/index.json` | |
| Directory rendered | `/o:{org}/research/` (uses `index.md` if present, otherwise a generated listing) | `text/html` |
| Directory source / JSON | `/o:{org}/research/index.md`, `/o:{org}/research/index.json` (a listing when there is no `index.md`) | |
| Site config | `/o:{org}/tmp.yaml` | `application/yaml; charset=utf-8` |
| Asset | `/o:{org}/assets/diagram.png` | image MIME, `Content-Disposition: inline` |
| Search (HTML) | `/o:{org}/search?q=...` | `text/html` |
| Search (JSON) | `/o:{org}/search.json?q=...&path_prefix=/research/&cursor=...` | `application/json` |

`/research` without a trailing slash redirects (307) to `/research/` when it is a directory. Old paths left by moves redirect (307) to the current path in the same representation. Redirects preserve the incoming address form. Raw and JSON responses carry an `ETag` and `X-Robots-Tag: noindex, nofollow, noarchive`.

Page JSON:

```json
{
  "org": "XS67DF65",
  "path": "/research/circle.md",
  "kind": "page",
  "title": "Circle",
  "description": "Research on Circle",
  "tags": ["community"],
  "order": 20,
  "revision": 3,
  "created_at": "2026-09-17T23:10:00Z",
  "updated_at": "2026-09-18T00:01:00Z",
  "content": "---\ntitle: Circle\n...",
  "urls": {"html": "https://.../o:XS67DF65/research/circle", "raw": "https://.../o:XS67DF65/research/circle.md", "json": "https://.../o:XS67DF65/research/circle.json"}
}
```

Directory listing JSON is `{"org", "directory": {...same fields without content...}, "entries": [...]}` for up to 100 children. Search JSON is `{"query", "hits": [{"path","title","snippet","revision","urls"}], "next_cursor"?}` with 20 hits per page; `snippet` marks matches with `**`.

JSON never includes email addresses, internal IDs, grant IDs, historical content or audit data.

## Filing (AI-assisted placement)

`POST /organize` answers "where does this belong?". The server takes a snapshot of the tenant's tree (folder paths and titles, page paths with titles and tags, file paths; at most 300 lines), sends it with the first 2,500 bytes of the content to a small model when `ANTHROPIC_API_KEY` is configured, and validates the answer: the folder and name must satisfy the path grammar, reserved roots are refused, and an occupied path is reported as `exists` with its `existing_revision` and a free numbered alternative. Without a key, or when the model fails, answers badly or the tenant's hourly budget (`AI_MAX_CALLS_PER_HOUR`) is spent, a deterministic heuristic runs instead: a folder whose name appears in the text, otherwise `/inbox`; the name from the frontmatter title, first heading or the filename hint; `.md` unless the hint carries a known file extension or the content is JSON.

```json
{
  "placement": {
    "path": "/research/circle-pricing.md", "directory": "/research", "name": "circle-pricing.md", "kind": "page",
    "title": "Circle pricing", "reason": "It compares Circle's plans, like the other research pages.",
    "source": "ai", "new_directory": false, "exists": false, "alternatives": ["/notes"], "expected_revision": 0
  },
  "write_with": {"path": "/research/circle-pricing.md", "expected_revision": 0}
}
```

`write_with.path` is always free: it is the suggestion, or its numbered variant when the suggestion is taken. Follow with `PUT /entries?path=...` and `If-None-Match: *`. Every model call is recorded in `ai_call` (purpose, model, token counts, latency, outcome; never content) and summarized on the admin overview.

## Status semantics

| Situation | Response |
|---|---|
| Browser navigation (GET/HEAD, `Sec-Fetch-Mode: navigate` or `Accept: text/html`, no bearer) to a shortcut or explicit URL without a session | 302 to `/login?return=<path>`; the return path is validated and same-origin only |
| Non-interactive request (raw, JSON, asset, `Accept: application/json`, or any bearer) without credentials to a shortcut URL | 401 |
| Explicit `/o:{org}` URL that the principal cannot access, or unknown code | 404 (indistinguishable from a missing path) |
| Known org, unknown path | 404 |
| Valid credential without `content:read` | 403 `insufficient_scope` |
| `/` signed out | landing page; `/` with a session or bearer serves the site root |

## REST endpoints

Base: `/api/v1/orgs/{org}` where `{org}` is the bare code, for example `/api/v1/orgs/XS67DF65`. All paths are query parameters or JSON fields, never URL segments.

Every mutation requires:

- `Idempotency-Key: <UUID>`. Missing: 428 `precondition_required`. Present but not a UUID: 422 `validation_failed`. Same key, same payload, same principal within 24 hours: the original response is returned without a new revision. Same key with a different payload or operation: 409 `idempotency_mismatch`. Another principal cannot replay your key.
- A precondition header. Create: `If-None-Match: *`. Update, move, delete, restore, asset replacement: `If-Match: "<etag>"` from a previous read. Missing: 428 `precondition_required`. Stale: 412 `revision_conflict` with `current_revision` in the body. An ETag issued for a different entry: 412 `revision_conflict` with the message `If-Match was issued for a different entry`.

ETags are opaque strings of the form `"e<entry-id-base36>-r<revision>"`. Both parts are checked: the entry identity must match the addressed entry and the revision must be current. Send them back unchanged. Cursors are opaque, HMAC-signed and bound to the query they came from; a cursor from another query is 422 `invalid cursor`.

| Method and path | Query / body | Headers | Success | Notes |
|---|---|---|---|---|
| `GET /entries` | `path` (required), `revision` (optional, historical) | | 200 `{entry, urls, content?, revision?}` + `ETag` | `content` for pages and config. Deleted entries are returned with `entry.deleted: true` to account readers. |
| `PUT /entries` | `path`; body `{"content": "...", "summary": "..."}` | `If-None-Match: *` to create, `If-Match` to replace; `Idempotency-Key` | 201 created, 200 replaced; `{entry, urls, created, unchanged, warnings?}` + `ETag` | Pages and `/tmp.yaml`. Identical content returns `unchanged: true` with no new revision. Missing parents are created. `summary` up to 500 chars. |
| `DELETE /entries` | `path` | `If-Match`, `Idempotency-Key` | 200 `{entry, urls}` | Soft delete of a page, asset or empty directory. Requires `content:delete`. Revokes the document's share links permanently. |
| `GET /tree` | `path` (default `/`), `cursor`, `limit` (1-100, default 100) | | 200 `{directory, entries: [{entry, urls}], next_cursor}` | Directories first, then files by path. |
| `POST /organize` | body `{"content": "...", "filename": "...", "hint": "..."}` | | 200 `{placement, write_with: {path, expected_revision: 0}}` | Suggests where new content belongs (see "Filing" below). Nothing is written. Requires `content:read`. Empty content: 422. |
| `POST /directories` | body `{"path": "/research"}` | `Idempotency-Key` | 201 created, 200 if it existed | Creates missing parents. |
| `POST /moves` | body `{"from": "/a.md", "to": "/b/c.md"}` | `If-Match` (of `from`), `Idempotency-Key` | 200 `{entry, urls, rewrites?, aliases}` | Pages and assets. Destination must be free. Leaves an alias at `from`. |
| `GET /search` | `q` (1-200 chars), `path_prefix`, `cursor`, `limit` (1-50, default 20) | | 200 `{query, hits, next_cursor}` | Current live pages only. |
| `GET /history` | `path`, `cursor`, `limit` (1-50, default 50) | | 200 `{entry, items: [{revision, operation, summary, actor, actor_kind, author_name, path, prior_path, size_bytes, created_at, share_label, title}], next_cursor}` | Newest first. Includes deleted entries. |
| `POST /restores` | body `{"path": "...", "revision": N}` | `If-Match`, `Idempotency-Key` | 200 mutation result | Creates a new revision from revision N. Revives a deleted page. Directories are not restorable. |
| `POST /assets` | `path` (`.png`/`.jpg`/`.jpeg`/`.webp`); multipart field `file`, or the raw image as the body | `If-None-Match: *` or `If-Match`; `Idempotency-Key` | 201 or 200 mutation result | Signature checked, decoded within limits, extension must match. Max `QUOTA_MAX_ASSET_BYTES`. |
| `GET /export` | | owner session only | 200 `application/zip`, `Content-Disposition: attachment; filename="tmp-{org}.zip"` | 5 per minute. Bearer tokens get 403. |
| `POST /share-links` | body `{"entry_id": N}` or `{"path": "/x.md"}`, optional `"label"` (80 chars) | owner session + CSRF only | 201 `{link: {id, entry_id, path, label, created_at, allowed_assets}, url, warning}` | `url` is the secret link, shown once. Only live pages. |
| `GET /share-links` | `entry_id` (optional) | owner session only | 200 `{links: [...]}` | Metadata only; never tokens. |
| `DELETE /share-links/{id}` | | owner session + CSRF | 204 | Revoke. Idempotent. |
| `POST /share-links/{id}/assets/refresh` | | owner session + CSRF | 200 `{link}` | Re-approve embedded assets. |

Mutation result shape:

```json
{
  "entry": {"id": 5, "path": "/research/circle.md", "kind": "page", "title": "Circle", "revision": 2,
            "updated_at": "...", "created_at": "...", "html_path": "/research/circle", "raw_path": "/research/circle.md", "json_path": "/research/circle.json"},
  "urls": {"html": "...", "raw": "...", "json": "..."},
  "created": false,
  "unchanged": false,
  "warnings": ["broken internal link: /research/missing"]
}
```

`urls` are absolute, explicit-form URLs on `APP_ORIGIN`. Success is sent only after the transaction commits.

## Errors

Envelope for every JSON error:

```json
{
  "error": {
    "code": "revision_conflict",
    "message": "the page changed since you read it; read the current revision, merge your changes and retry with expected_revision set to it",
    "current_revision": 4,
    "expected_revision": 3,
    "field_errors": [{"field": "frontmatter.title", "line": 2, "message": "..."}],
    "suggested_path": "/research/circle.md"
  },
  "request_id": "01J..."
}
```

Optional members appear only when set. Messages for 5xx are replaced by `internal error`; stack traces are never returned.

| Code | HTTP | When |
|---|---|---|
| `invalid_path` | 400 | grammar violation, reserved segment, uppercase (with `suggested_path`), wrong extension for the operation |
| `unauthorized` | 401 | missing or invalid credential |
| `insufficient_scope`, `forbidden` | 403 | scope missing; owner-session-only operation attempted with a token; CSRF or Origin failure; cross-origin write |
| `not_found` | 404 | unknown path, unknown or foreign org, unknown revision, revoked link |
| `path_conflict`, `idempotency_mismatch` | 409 | destination occupied, rendered-namespace collision, alias reuse, kind mismatch; key reused with a different payload |
| `revision_conflict` | 412 | `If-Match` does not match the current revision, was issued for a different entry, or is malformed; also `If-None-Match: *` on an existing entry |
| `payload_too_large` | 413 | page, config or asset over its limit |
| `validation_failed` | 422 | frontmatter, extension, link or `tmp.yaml` errors (`field_errors`), bad JSON body, `Idempotency-Key` that is not a UUID, non-empty directory delete, bad cursor |
| `precondition_required` | 428 | no `If-Match` / `If-None-Match`; no `Idempotency-Key` on a mutation |
| `rate_limited` | 429 | with `Retry-After` seconds |
| `quota_exceeded` | 507 | tenant quota reached; message says which |
| `internal_error` | 500 | unexpected failure |

Method not allowed is 405 with `Allow`. Requests whose body exceeds the global limit (`QUOTA_MAX_ASSET_BYTES` + 1 MiB) may be cut off by the server with a plain 413.

## Rate limits

Per minute, sliding window, in memory.

| Routes | Limit | Key |
|---|---|---|
| REST reads (`GET /entries`, `/tree`, `/search`, `/history`) and content URLs | 300 | principal, else IP |
| REST mutations and dashboard mutations | 60 | principal, else IP |
| `/export` and `/admin/export.zip` | 5 | principal |
| Sign-in and token endpoints | 30 | IP |
| `/mcp` | 300 | IP |

## curl examples

Set up variables. `$TOKEN` is a `tmpk_` token from Connections; `$ORG` is the code shown on the dashboard.

```bash
H=http://localhost:8000
AUTH="Authorization: Bearer $TOKEN"
API=$H/api/v1/orgs/$ORG
```

Create a page:

```bash
curl -i -X PUT "$API/entries?path=/research/circle.md" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -H 'If-None-Match: *' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"content":"---\ntitle: Circle\n---\n\n# Circle\n\nFirst notes.\n","summary":"create"}'
# HTTP/1.1 201 Created, ETag: "e5-r1"
```

Replace it (read the ETag first):

```bash
ETAG=$(curl -si "$API/entries?path=/research/circle.md" -H "$AUTH" | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r')
curl -i -X PUT "$API/entries?path=/research/circle.md" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -H "If-Match: $ETAG" -H "Idempotency-Key: $(uuidgen)" \
  -d '{"content":"---\ntitle: Circle\n---\n\n# Circle\n\nSecond draft.\n","summary":"expand"}'
# 200 OK, ETag: "e5-r2"
```

Conflict (reusing the stale ETag):

```bash
curl -i -X PUT "$API/entries?path=/research/circle.md" \
  -H "$AUTH" -H 'Content-Type: application/json' \
  -H "If-Match: $ETAG" -H "Idempotency-Key: $(uuidgen)" \
  -d '{"content":"# Circle\n\nStale.\n"}'
# 412 Precondition Failed
# {"error":{"code":"revision_conflict","message":"...","current_revision":2,"expected_revision":1},"request_id":"..."}
```

List the tree and search:

```bash
curl -s "$API/tree?path=/research" -H "$AUTH"
curl -s "$API/search?q=circle&path_prefix=/research/" -H "$AUTH"
```

History and restore revision 1:

```bash
curl -s "$API/history?path=/research/circle.md" -H "$AUTH"
curl -i -X POST "$API/restores" -H "$AUTH" -H 'Content-Type: application/json' \
  -H 'If-Match: "e5-r2"' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"path":"/research/circle.md","revision":1}'
# 200, entry.revision = 3
```

Move:

```bash
curl -i -X POST "$API/moves" -H "$AUTH" -H 'Content-Type: application/json' \
  -H 'If-Match: "e5-r3"' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"from":"/research/circle.md","to":"/notes/circle.md"}'
# 200, "aliases":["/research/circle.md"]; GET /o:$ORG/research/circle now 307s to /o:$ORG/notes/circle
```

Upload an asset (multipart):

```bash
curl -i -X POST "$API/assets?path=/assets/diagram.png" -H "$AUTH" \
  -H 'If-None-Match: *' -H "Idempotency-Key: $(uuidgen)" \
  -F file=@diagram.png
# 201 Created
```

Export (owner session cookie; a bearer token is refused):

```bash
curl -sS -o site.zip -b "tmp_session=$SESSION" "$API/export"
```

Create and revoke a share link (owner session; needs the CSRF token from the dashboard and a same-origin `Origin`):

```bash
curl -s -X POST "$API/share-links" -b "tmp_session=$SESSION" \
  -H "X-CSRF-Token: $CSRF" -H "Origin: $H" -H 'Content-Type: application/json' \
  -d '{"path":"/notes/circle.md","label":"reviewer"}'
# 201 {"link":{"id":7,...},"url":"http://localhost:8000/s/<token>","warning":"Anyone with this link can view and edit this document."}

curl -i -X DELETE "$API/share-links/7" -b "tmp_session=$SESSION" -H "X-CSRF-Token: $CSRF" -H "Origin: $H"
# 204 No Content
```

Read content with the API token at the shortcut form:

```bash
curl -s "$H/notes/circle.md" -H "$AUTH"          # raw Markdown
curl -s "$H/notes/circle.json" -H "$AUTH"        # JSON
curl -s "$H/o:$ORG/notes/circle.json" -H "$AUTH" # same entry, explicit form
```
