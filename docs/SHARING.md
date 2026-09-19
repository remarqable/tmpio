# Sharing

A secret link grants anyone holding it read and write access to one Markdown document. No account, Google login, OAuth connection or API key is involved. The implementation is `internal/models/share.go` and `internal/controllers/share.go`.

## The model

| Property | Behaviour |
|---|---|
| Capability | Always read plus write on one document. There is no read-only mode and no site-wide mode. |
| Scope | Exactly one live Markdown page. Directories, assets and `/tmp.yaml` cannot be shared. |
| Token | 32 cryptographically random bytes, unpadded base64url (43 characters). The URL is `APP_ORIGIN/s/{token}`. |
| Storage | Only the SHA-256 hash is stored. The plaintext is shown once at creation (dashboard page or `POST /share-links` response) and cannot be retrieved later. |
| Label | Optional, up to 80 characters, visible to the owner in link management and in history. Not secret. |
| Binding | The grant references the page's stable entry ID, not its path. A move does not break the link. |
| Expiry | None. Every link stays valid until revoked. |
| Revocation | Owner action, immediate. Every route under `/s/{token}` then returns 404. |
| Deletion | Deleting the document revokes all of its links permanently. Restoring the document does not reactivate them; create new links. |
| Replacement | Revoke, then create. Multiple labeled links per document are allowed. |
| Management | Owner browser session only (dashboard `/app/share?path=...`, or `/api/v1/orgs/{org}/share-links` with cookie plus CSRF). API tokens and MCP tokens cannot create, list, refresh or revoke links. |
| Audit | `share.create`, `share.revoke`, `share.assets_refresh` audit events. |

A signed-in owner who opens a `/s/{token}` URL acts as the link principal for that request, not as the owner. Ambient cookies cannot expand or rescue a token.

## HTTP contract

All routes validate the token before touching content. Unknown, malformed (fewer than 20 or more than 128 characters, or containing `/`, `.` or `%`), revoked, and deleted-document tokens are indistinguishable: 404. Raw, JSON and asset routes return the JSON error envelope; HTML routes return the error page. No route ever redirects to login.

| Method and route | Required headers | Success | Errors |
|---|---|---|---|
| `GET`, `HEAD /s/{token}` | | 200 HTML, themed, no site navigation, Edit action, link to instructions; `ETag` | 404 |
| `GET`, `HEAD /s/{token}/raw` | | 200 `text/markdown; charset=utf-8`, exact current source with frontmatter; `ETag` | 404 |
| `GET /s/{token}/json` | | 200 `{title, description, tags, content, revision, etag, updated_at, urls {html, raw, json, write, edit, instructions}, access: "read+write via sharing link"}`; `ETag` | 404 |
| `PUT /s/{token}/raw` | `Content-Type: text/markdown` (or `text/plain`), `If-Match: "<etag>"`, `Idempotency-Key: <UUID>`; optional `X-Author-Name`, `X-Change-Summary`; `Origin` if sent must equal `APP_ORIGIN` | 200 `{revision, etag, unchanged, warnings, urls {html, raw, json}}`; `ETag` | 403 cross-origin; 404 revoked or deleted; 409 key reused with a different body; 412 stale `If-Match`, or an ETag that is not this document's (`If-Match is not the ETag of this document`); 413 over `QUOTA_MAX_PAGE_BYTES`; 422 wrong Content-Type, `Idempotency-Key` that is not a UUID, invalid Markdown (`field_errors` with lines); 428 missing `If-Match` or missing `Idempotency-Key`; 429 with `Retry-After`; 507 quota |
| `GET /s/{token}/edit` | | 200 HTML editor: textarea, preview and save controls, current revision, token-scoped CSRF nonce, request ID | 404 |
| `POST /s/{token}/edit` | form fields `content`, `expected_revision`, `request_id`, `nonce`, optional `author_name`, `summary`; same-origin `Origin`/`Referer` | 303 to `/s/{token}/edit?saved=<revision>` | 403 "the form expired" on bad nonce or origin (text preserved in the textarea); 404; 412 conflict re-rendered in the editor with the current revision; 422 validation errors re-rendered |
| `POST /s/{token}/preview` | form field `content`; same-origin `Origin`/`Referer` | 200 HTML fragment rendered with the link's asset restrictions; nothing is saved | 403 cross-origin; 422 with the error list as HTML |
| `GET`, `HEAD /s/{token}/assets/{id}` | | 200 image bytes, `Content-Disposition: inline` | 404 unless the asset is approved and currently embedded |
| `GET /s/{token}/instructions` | | 200 `text/markdown`: the read, write and format contract for this document only | 404 |
| any other method on these routes | | | 405 with `Allow: GET, HEAD` |
| any other path under `/s/{token}/...` | | | 404; extra selectors such as `/s/{token}/o:CODE/x.md` never reach content routing |

Responses under `/s/` carry `Cache-Control: no-store`, `Referrer-Policy: no-referrer` and `X-Robots-Tag: noindex, nofollow, noarchive`, on errors as well. `robots.txt` disallows `/s/`. Shared pages load no third-party scripts or analytics.

## Machine flow

```bash
S=https://tmp.example.com/s/<token>

# 1. read; keep the ETag
curl -sD headers.txt -o current.md "$S/raw"
ETAG=$(awk 'tolower($1)=="etag:"{print $2}' headers.txt | tr -d '\r')

# 2. edit current.md locally, then replace the whole document
curl -si -X PUT "$S/raw" \
  -H 'Content-Type: text/markdown' \
  -H "If-Match: $ETAG" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H 'X-Author-Name: Review bot' \
  -H 'X-Change-Summary: fixed the pricing table' \
  --data-binary @current.md
# 200 {"revision":3,"etag":"\"e5-r3\"","unchanged":false,"warnings":null,"urls":{...}}
```

- `X-Author-Name` is optional, trimmed, cut to 80 characters, control characters removed. It is stored as an unverified display name and never presented as an authenticated identity.
- `X-Change-Summary` becomes the revision summary (up to 500 characters).
- Retrying the same `Idempotency-Key` with the same body returns the original result for 24 hours while the grant is valid. A revoked grant cannot replay a cached success: the request is 404.
- A PUT can only update the linked document. It cannot create, retarget, move or delete anything.
- Non-browser clients may omit `Origin`. If they send it, it must equal `APP_ORIGIN`.

An AI that can fetch URLs can read the document. Writing requires the ability to issue an HTTP PUT with headers. Browsing alone does not imply write capability.

## Browser editor

`GET /s/{token}/edit` renders a textarea with the current source, the current revision as `expected_revision`, a fresh UUID `request_id`, and a `nonce`. The nonce is `grantID.expiry.HMAC-SHA256(SESSION_SECRET, "share-csrf:" + grantID.expiry)` truncated, valid for two hours and bound to this grant. Saving posts the form. Preview posts to `/preview` through HTMX and returns a fragment; it never saves.

Rules enforced on save: same-origin check on `Origin`, or `Referer`, or `Sec-Fetch-Site`; valid nonce for this grant; valid revision. A conflict (412) re-renders the editor with the current revision number and the user's text; a validation failure (422) re-renders with line-numbered errors and the user's text. Revocation while the editor is open makes the next save a 404 page; the text is not lost.

Client-side draft handling (`static/js/site.js`): every keystroke saves the textarea to `localStorage` under `tmp-draft:<path>`. On reload, if a stored draft differs from the server content, a note offers "Restore draft". A successful save clears the draft. Drafts live only in that browser.

## Embedded assets

Local images in a shared document are served through `/s/{token}/assets/{asset-entry-id}` under three conditions, all checked on every request:

1. The asset entry ID is on the grant's allowlist (`share_grant_asset`).
2. The asset is live (not deleted) and is an asset entry with a blob.
3. The current revision of the document still embeds it. Image references are found with the Markdown parser, not text matching.

The allowlist is recorded when the owner creates the link: every local image the document embeds at that moment that resolves to a live asset. The owner can refresh it later from the Share page (or `POST /share-links/{id}/assets/refresh`) after reviewing the document. A link holder cannot enlarge the allowlist: a private asset path added by a recipient renders as `Image unavailable: <name>` with a warning in preview and on save, and no bytes are served. Removing an image from the document removes its access immediately. Deleting an asset blocks it. Replacing an approved asset (same path) serves the latest bytes.

External `https://` images remain external and are never fetched by the server. There is no asset listing, path resolver, upload or replacement through a link.

## What recipients cannot do

- Read any other page, directory listing, search, `/tmp.yaml`, history or deleted content. `tmp_history`, revisions and the trash are owner and account-client operations.
- Move, delete, restore or create documents, or edit site configuration.
- Learn the private canonical path or organization code from the JSON (`urls` stay under `/s/{token}`). The rendered HTML does contain the document's internal links as explicit `/o:{org}/...` URLs, because the owner wrote them; those URLs still require account authorization.
- Create, list or revoke links, or mint a new token. Forwarding the URL is possible; managing it is not.
- Use the token as an API or MCP credential, or select another entry or tenant with it.
- Bypass rate limits or quotas. Tenant quotas apply to link writes.

## Headers set on shared responses

| Header | Value |
|---|---|
| `Cache-Control` | `no-store` |
| `Referrer-Policy` | `no-referrer` |
| `X-Robots-Tag` | `noindex, nofollow, noarchive` |
| `X-Content-Type-Options` | `nosniff` (global) |
| `Content-Security-Policy` | the global policy: `default-src 'self'; script-src 'self'; ... frame-ancestors 'none'` |
| `ETag` | on HTML, raw, JSON and successful PUT |

## Rate limits

Two limits apply to every share route, both per minute:

| Routes | Per IP | Per grant |
|---|---|---|
| `GET`/`HEAD` HTML, raw, JSON, edit, assets, instructions | 120 | 120 |
| `PUT /raw`, `POST /edit`, `POST /preview` | 30 | 30 |

Exceeding either returns 429 with `Retry-After` in seconds and the `rate_limited` envelope. The per-grant limit is checked after token validation, so unknown tokens cannot consume a real grant's budget. The per-grant limiters are created once per process (`sync.Once`) and shared by all share routes.

## Attribution in history

Revisions written through a link record `actor_kind: share_link`, the grant ID, the optional author name, and the summary. Owners see them in History and on the dashboard as **Anonymous via sharing link** with the link's label (`share_label`) and the unverified `author_name` when supplied. No user ID is stored for link edits. The JSON of a revision list item:

```json
{"revision": 2, "operation": "update", "summary": "fixed the pricing table",
 "actor": "Anonymous via sharing link", "actor_kind": "share_link", "author_name": "Review bot",
 "share_label": "reviewer", "path": "/research/circle.md", "size_bytes": 812, "created_at": "..."}
```
