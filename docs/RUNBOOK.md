# Runbook

Operational procedures for a single-node tmp deployment. Commands assume the environment file is exported (`set -a; . config/local.env; set +a`) or that you are running under `make`, which does this for you.

## Credential lookups on a managed database

Locally `app_owner` is the cluster superuser, so row security never applies to it. On a managed database it is an ordinary `NOBYPASSRLS` role, and the `SECURITY DEFINER` lookups (`lookup_share_grant`, `lookup_api_token`, `lookup_oauth_token`, `lookup_oauth_code`) execute as that role. Migration 8 lets `app_owner` read the five credential tables without a tenant scope; without it every OAuth code, access token, API token and sharing link fails with `invalid_grant` or 401 while page reads keep working. Symptom in the journal: `oauth.token_rejected` with `code is invalid, expired or already used` on the first use of a fresh code.

## Start and stop

| Task | Development | Production (systemd) |
|---|---|---|
| Start | `make db-start` then `make run` | `sudo systemctl start tmp` |
| Stop | Ctrl-C, then `make db-stop` | `sudo systemctl stop tmp` |
| Restart | stop and start | `sudo systemctl restart tmp` |
| Logs | stdout (console format) | `journalctl -u tmp -f` (JSON lines) |

The process handles SIGINT and SIGTERM with a 30-second graceful shutdown. In-flight requests finish; new connections are refused. Server timeouts: 10 s read header, 60 s read, 120 s write, 120 s idle.

Startup order matters: the database must accept connections and migrations must be applied. The server syncs preregistered OAuth clients into `oauth_client` at boot and exits with `oauth clients (did you run migrations?)` when the table is missing.

## Migrations

Migrations are goose SQL files in `migrations/`. They run as `app_owner`, never at server startup.

```bash
make migrate                      # up on tmp and tmp_test
make migrate-status               # goose status on tmp
goose -dir migrations postgres "$DATABASE_OWNER_URL" status
```

Rollback is validated on the test database only:

```bash
make migrate-down                 # goose down (one step) on tmp_test
make db-reset                     # down-to 0 then up on tmp_test
```

To roll back production, take a backup first, stop the service, run `goose ... down` against `DATABASE_OWNER_URL`, and deploy the matching older binary. The three shipped migrations drop tables on down; data in those tables is lost.

Adding a migration: `goose -dir migrations create <name> sql`. Every tenant-scoped table needs `tenant_id`, `ENABLE` and `FORCE ROW LEVEL SECURITY`, and one policy with both `USING` and `WITH CHECK`, following `00002_content.sql`.

## Backup

Back up daily with `pg_dump` under the owner URL. The dump includes everything: users, identities, sessions (hashes), tenants, entries, every revision source, asset bytes (`asset_blob.data`), share grants (hashes), OAuth grants and token hashes, API token hashes, audit events, mutation receipts and the launch waitlist. No plaintext credential exists in the database, so the dump contains none. It does contain user content and email addresses, so the dump is encrypted before it touches disk and the plaintext never exists as a file.

The backup key is an [age](https://age-encryption.org) key pair. The public (recipient) key lives on the server; the private key lives only with the operator, off the server, alongside the session secret. Without it a backup is unreadable, including to us.

```bash
# once, on the operator's machine: age-keygen -o tmp-backup.key; copy the "public key:" line to the server
# daily on the server, keep 14
pg_dump --format=custom --no-owner --no-privileges "$DATABASE_OWNER_URL" \
  | age -r "$TMP_BACKUP_RECIPIENT" -o "/var/backups/tmp/tmp-$(date +%F).dump.age"
find /var/backups/tmp -name 'tmp-*.dump.age' -mtime +14 -delete
```

Restore decrypts first: `age -d -i tmp-backup.key tmp-2026-09-18.dump.age > tmp-2026-09-18.dump`, then `pg_restore` as below, then delete the plaintext file. The managed database's own automated backups are encrypted by the provider and are the first recovery path; these dumps are the second.

Custom format is compressed and supports selective restore. Point-in-time recovery (WAL archiving) is a PostgreSQL configuration decision outside this runbook.

## Launch waitlist

While `SIGNUPS_ENABLED=0`, the landing page collects email addresses into `launch_signup`. Nothing in the application reads that table. To export it for the launch email, as `app_owner`:

```sql
SELECT email, source, created_at FROM launch_signup ORDER BY created_at;
```

Delete the rows after the launch email has been sent (the privacy notice promises this): `DELETE FROM launch_signup;`. To remove one address on request: `DELETE FROM launch_signup WHERE email = lower('person@example.com');`.

## Account deletion

Owners delete their own account from Site settings by typing their organization code. `models.DeleteAccount` deletes the tenant row and the user row in one transaction; `ON DELETE CASCADE` removes every entry, revision, asset blob, share grant, API token, OAuth grant, token and code, audit event and receipt, then the user's identities and sessions. Referential actions bypass row security, so the cascade is complete. The deletion is logged as `account.deleted` with the tenant ID only.

An operator can do the same for a user who cannot sign in, as `app_owner`, after verifying the request out of band:

```sql
BEGIN;
DELETE FROM tenant WHERE id = (SELECT tenant_id FROM membership WHERE user_id = :user_id AND role = 'owner');
DELETE FROM "user" WHERE id = :user_id;
COMMIT;
```

Deleted data remains in encrypted dumps until they age out (14 days) and in the provider's automated backups for their retention window.

## Restore drill

Run this before real pilot data exists and after every schema change. Restore into a scratch database, never over the live one.

```bash
createdb -h 127.0.0.1 -p 5433 -U app_owner tmp_restore_check
pg_restore --no-owner --no-privileges -d "postgres://app_owner:app@127.0.0.1:5433/tmp_restore_check?sslmode=disable" \
  /var/backups/tmp/tmp-2026-09-18.dump
```

Verify counts against the source:

```sql
SELECT (SELECT count(*) FROM tenant)      AS tenants,
       (SELECT count(*) FROM entry)       AS entries,
       (SELECT count(*) FROM revision)    AS revisions,
       (SELECT count(*) FROM asset_blob)  AS blobs,
       (SELECT count(*) FROM share_grant) AS share_grants,
       (SELECT max(created_at) FROM revision) AS newest_revision;
```

Then confirm RLS survived the restore. As `app_owner`:

```sql
SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class
 WHERE relname IN ('entry','revision','share_grant','oauth_token') ORDER BY 1;
```

All four must show `t, t`. Optionally point a second server instance at the scratch database with `DATABASE_URL` as `app_user` and load a page. Drop the scratch database afterwards.

Recovery objectives proposed for the pilot: at most 24 hours of data loss, restoration within four hours.

**Drill performed 2026-09-18 on the local cluster.** `pg_dump -Fc` of the `tmp` database was restored into a scratch database. Counts matched the source exactly: 28 entries, 35 revisions, 4 tenants, 3 share grants. All 13 tenant-scoped tables retained `FORCE ROW LEVEL SECURITY` after the restore.

## Rotating SESSION_SECRET

`SESSION_SECRET` is an HMAC key for two things only:

- the CSRF nonce embedded in the secret-link editor form (`/s/{token}/edit`), valid two hours;
- pagination cursors returned by `tree`, `search` and `history`.

Rotating it invalidates open share-editor forms (the user sees "the form expired; copy your text, reload and try again"; their text is kept in the textarea and in browser localStorage) and any cursor a client is holding (`422 validation_failed: invalid cursor`). It does not invalidate browser sessions, API tokens, OAuth tokens or share links. Those are stored as SHA-256 hashes in the database.

Procedure: change the value in the environment file, restart the service. Nothing else is needed.

## Revoking credentials

| Credential | Where | Effect |
|---|---|---|
| Client connection (OAuth grant) | Admin, Connections (`/admin/connections`), Revoke. No REST endpoint. | Grant and every access and refresh token under it are revoked in one transaction. The next MCP request on an existing session gets 401. |
| REST API token (`tmpk_`) | Dashboard, Connections, Revoke next to the token. | Immediate 401 on the next request. |
| Secret link | Dashboard, Share page for the document (`/app/share?path=...`), Revoke; or `DELETE /api/v1/orgs/{org}/share-links/{id}` with the owner session and CSRF header. | HTML, raw, JSON, edit, preview, assets and instructions return 404. Cached idempotent results for that grant are no longer replayed. |
| Browser session | Sign out (`POST /logout`) revokes the current session. | There is no "sign out everywhere" UI. Operator SQL: `UPDATE session SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;` as `app_owner`. |

Deleting a page revokes its share links permanently. Restoring the page does not reactivate them.

## Quota errors and operator purge

Quota failures return `507 Insufficient Storage` with code `quota_exceeded` on REST, and `quota_exceeded` on MCP. The message names the quota. Limits are per tenant and configurable through the `QUOTA_*` variables (see SETUP). History is never discarded to admit a write.

There is no purge command in this build. Two quotas can require operator action:

- `QUOTA_MAX_RETAINED_BYTES` (default 500 MiB): the sum of `revision.size_bytes` plus `asset_blob.size_bytes` for the tenant.
- `QUOTA_MAX_CURRENT_ASSET_BYTES` (default 100 MiB): the sum of live asset sizes. The owner can delete assets themselves; deletion keeps the blob in history.

First ask the owner to export (`/admin/export`) and delete what they no longer need. If the retained quota still blocks writes, the operator can remove old revisions at the SQL level.

**Warning.** This is destructive and bypasses the application. It runs as `app_owner`, which bypasses RLS, so a mistake in the `tenant_id` predicate damages another tenant. Take a backup first. Never delete a revision whose `seq` equals `entry.current_revision`. Never delete a revision that a live share grant's document currently uses. Restores of deleted revisions become impossible.

```sql
BEGIN;
-- tenant to purge
\set tid 42
-- 1. See what would go: revisions older than 90 days that are not current.
SELECT r.id, r.entry_id, r.seq, r.operation, r.size_bytes, r.created_at
  FROM revision r JOIN entry e ON e.id = r.entry_id AND e.tenant_id = r.tenant_id
 WHERE r.tenant_id = :tid AND r.seq <> e.current_revision
   AND r.created_at < now() - interval '90 days'
 ORDER BY r.created_at LIMIT 50;
-- 2. Blank the source of those revisions instead of deleting rows, so history
--    metadata and sequence numbers stay intact. size_bytes drives the quota.
UPDATE revision r SET source = NULL, size_bytes = 0
  FROM entry e
 WHERE e.id = r.entry_id AND e.tenant_id = r.tenant_id
   AND r.tenant_id = :tid AND r.seq <> e.current_revision
   AND r.created_at < now() - interval '90 days' AND r.source IS NOT NULL;
-- 3. Asset blobs referenced by no current entry and no remaining revision.
DELETE FROM asset_blob b
 WHERE b.tenant_id = :tid
   AND NOT EXISTS (SELECT 1 FROM entry e WHERE e.tenant_id = b.tenant_id AND e.blob_id = b.id)
   AND NOT EXISTS (SELECT 1 FROM revision r WHERE r.tenant_id = b.tenant_id AND r.blob_id = b.id);
COMMIT;
```

A blanked revision shows in history but cannot be restored (`that revision has no restorable content`). Record the purge in your change log; the application audit table does not see it.

## Health, readiness, metrics

| Route | Purpose | Protection |
|---|---|---|
| `GET /healthz` | liveness; touches nothing | none; returns `{"status":"ok"}` |
| `GET /readyz` | readiness; pings the database with a 2 s timeout | none; returns 200 `{"status":"ok"}` or 503 `{"status":"degraded","db":"unavailable"}` without the raw error |
| `GET /metrics` | Prometheus text format | with `METRICS_TOKEN` set: `Authorization: Bearer <token>` required. Without it: loopback clients only. Anything else gets 404. |

Point the orchestrator's liveness probe at `/healthz` and load balancers at `/readyz`.

Metrics exposed: `tmp_uptime_seconds`, `tmp_http_requests_total{route,method,class}`, `tmp_http_request_duration_seconds` histogram by route, and counters `tmp_auth_failures_total` (401s), `tmp_write_conflicts_total` (412s), `tmp_rate_limited_total` (429s). Labels are route templates and status classes only. No tenant, user or entry identifiers appear.

## Logs

Logging is zerolog, one line per request (`event: http.request`) with `method`, `route`, `status`, `took` and the correlation fields `request_id`, `tenant_id`, `user_id` where known. Development uses the console writer; any other `APP_ENV` writes JSON to stdout.

Rules enforced in code:

- Tokens, authorization codes, session cookies, page bodies and query strings are never logged.
- Secret-link routes are logged by template, `/s/:token`, `/s/:token/raw`, `/s/:token/edit`, and so on. The token never appears. Grant identity is available through the audit table by grant ID.
- Content catch-all requests log `route=content`, not the path.
- Other events: `auth.signed_in` (user_id, created), `auth.google_failed`, `oauth.refresh_failed`, `mcp.mutation` (path, revision), `internal error` with the wrapped cause.
- Clients may send `X-Request-ID` (up to 64 chars); otherwise a ULID is assigned. The response echoes it in `X-Request-ID` and error envelopes include it as `request_id`.

Configure the reverse proxy to redact `/s/<token>` paths in its own access log, or disable access logging for `/s/`. The application cannot control that log.

## Incidents

### Leaked share link

1. Find the grant: Dashboard, Content, open the document, Share. Or `GET /api/v1/orgs/{org}/share-links?entry_id=...` with an owner session.
2. Revoke it. All routes under `/s/{token}` return 404 from the next request, including open editors and asset loads. Idempotent replays under that grant stop.
3. Check history (`/history/<page>`) for edits attributed to "Anonymous via sharing link" with that label. Restore an earlier revision if needed.
4. Create a new link if sharing should continue. Replacement is always revoke then create.

Revocation cannot retract copies already downloaded.

### Leaked API token or client credential

1. Dashboard, Connections. Revoke the token (`tmpk_`) or the connection.
2. Review recent history on the dashboard for writes by "API token" or by the client's name.
3. Search the audit table if needed (as `app_owner`): `SELECT * FROM audit_event WHERE api_token_id = $1 ORDER BY created_at DESC;`

MCP access tokens (`tmpa_`) live 15 minutes; refresh tokens (`tmpr_`) rotate and any reuse revokes the whole family automatically. Revoking the connection covers both.

### Suspected cross-tenant issue

1. Confirm the running process connects as `app_user`: `SELECT current_user, rolbypassrls FROM pg_roles WHERE rolname = current_user;` from a psql session using `DATABASE_URL`. `rolbypassrls` must be `f`.
2. Confirm every content table has forced RLS: `SELECT relname FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind = 'r' AND relname NOT IN ('user','identity','session','tenant','oauth_client','login_state','goose_db_version') AND NOT relforcerowsecurity;` must return no rows.
3. Run the isolation tests against the test database: `set -a; . config/local.env; set +a; go test ./internal/models/ -run 'TestEveryTenantTableHasRLS|TestTenantIsolation' -race -count=1`. They connect as `app_user` and prove read, insert, update and delete are blocked across tenants, that composite foreign keys reject foreign-tenant references, and that the unscoped handle returns zero rows. For a manual probe against production data, as `app_user` in one transaction: `SELECT set_config('app.tenant_id','<A>',true); SELECT count(*) FROM entry WHERE tenant_id = <B>;` must return 0.
4. `make check` must pass: no `db.Unscoped()` in `internal/controllers`, `internal/models` or `internal/mcp`.
5. If a leak is confirmed, stop the service, preserve logs and a database snapshot, and treat all share tokens and credentials as exposed.
