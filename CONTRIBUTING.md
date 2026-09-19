# Contributing to tmp

Thank you for looking at this. tmp is small on purpose: one Go binary, PostgreSQL, server-rendered HTML, no build step. Contributions that keep it that way are the easiest to accept.

## Before you start

- **Security issues** go to dev@remarqable.io, not to the issue tracker. See [SECURITY.md](SECURITY.md).
- **Bugs and small fixes**: open a pull request directly. A failing test that shows the bug is the best first commit.
- **Features and behaviour changes**: open an issue first and describe the problem, not just the solution. The "Status and known limitations" section of the README explains what is deliberately not built, and the pattern docs in `blueprint/` explain how it is built. A change that widens the attack surface (new ways to run code, fetch URLs, accept HTML, or bypass compare-and-swap writes) needs a strong reason.

## Development

Follow the Quick start in the README: a project-local PostgreSQL 16, `make migrate`, `make run`. Then:

```bash
make ci        # fmt, vet, build, blueprint boundary checks, full test suite
make test-unit # no database needed
```

The full suite runs against the `tmp_test` database as the RLS-enforced `app_user` role. Tests that touch the database live next to the code and skip when `TEST_DATABASE_URL` is unset.

## Architecture rules

The code follows the blueprint in `blueprint/` (a read-only submodule). The rules that reviewers will check:

- Controllers adapt HTTP to model calls and render. They do not contain business logic or SQL.
- Models own domain logic and every database query. Every content query runs inside `db.WithTenant` or `db.WithTenantSerialized`; there is no code path that reads content outside a tenant scope. `make check` fails if `db.Unscoped()` appears in request-handling packages.
- Every table with a `tenant_id` has `FORCE ROW LEVEL SECURITY` and a policy with both `USING` and `WITH CHECK`. A test enumerates `pg_class` and fails otherwise.
- Tokens are compared by hash and never logged. Page bodies, query strings and request bodies never reach the log.
- Rendered Markdown passes through the sanitizer allowlist in `internal/platform/render/sanitize.go`. Raw HTML in pages is rejected, not stripped.
- User-facing strings go through the i18n catalog (`internal/platform/i18n/catalog/en.json`).
- Schema changes are goose migrations in `migrations/` with a working `Down`.

## Pull requests

- Keep each pull request to one change. Include tests for behaviour you add or fix.
- Run `make ci` before pushing.
- Describe what changed and why in the pull request; link the issue if there is one.
- By contributing you agree that your contribution is licensed under the MIT license in [LICENSE](LICENSE).

## Code of conduct

Be kind and direct. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
