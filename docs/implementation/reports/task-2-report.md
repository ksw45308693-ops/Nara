# Task 2 report — identity, tenant storage, and digest

## Delivered files

- `internal/auth/auth.go`, `internal/auth/auth_test.go`
- `internal/store/migrations.go`, `internal/store/migrations_test.go`, `internal/store/schema_contract_test.go`, `internal/store/deliveries.go`, `internal/store/deliveries_test.go`, `internal/store/pgx_adapter.go`, `internal/store/pgx_adapter_test.go`
- `internal/digest/digest.go`, `internal/digest/digest_test.go`
- `migrations/0001_initial.sql`

## Behavior

- Roles are restricted to `platform_admin`, `tenant_admin`, and `member`.
- Passwords use bcrypt. Sessions use 32-byte opaque client tokens; only a SHA-256 hash is intended for storage. CSRF comparison uses `crypto/subtle`.
- The initial PostgreSQL migration keeps notices shared and scopes tenants, users, filters, matches, schedules, recipients, deliveries, and job runs by tenant. Sessions deliberately derive tenancy only through users and are excluded from RLS; two narrow authentication lookup functions handle password and session bootstrap.
- The migration runner records and skips completed versions through a pgx-compatible transaction adapter.
- Digest output is escaped HTML and RFC 5322 UTF-8 MIME. Daily due/catch-up calculations use `Asia/Seoul`; the retry helper attempts at most three times.

## RED/GREEN evidence

- RED: the first package tests failed with undefined auth, digest, and store symbols.
- GREEN: implementing the primitives made auth and digest pass; the migration fake initially exposed the expected statement-count contract and then passed after the runner was completed.
- RED: schema contract test failed for absent composite tenant foreign keys, then for absent `FORCE ROW LEVEL SECURITY`.
- GREEN: the migration now satisfies both SQL contracts.
- RED: MIME digest test failed before `BuildSMTPMessage` existed, then caught bare-address header formatting.
- GREEN: it passes with validated RFC 5322 headers and HTML UTF-8 body.

## Verification

`go test ./internal/auth ./internal/digest ./internal/store` — PASS

`go vet ./internal/auth ./internal/digest ./internal/store` — PASS

## Assumptions and concerns

- No PostgreSQL server is available. SQL asset loading, DDL execution, RLS behavior, and migration execution against a real pgx connection remain unverified.
- Integration must set `app.tenant_id` per tenant-scoped database transaction. The serving runtime role is `NOBYPASSRLS`; it must never use migration/owner credentials.
- No SMTP server is available. MIME construction, escaping, retry behavior, and fakes are locally verified; SMTP authentication, TLS, and delivery are not. Task 4 should connect `BuildSMTPMessage` to the configured SMTP transport.

## Fix round 1 — review rulings

- `sessions` now contains only `user_id`, token hash, expiry, and timestamps. It has no tenant column, index, RLS enablement, forced RLS, or policy. Authentication calls narrowly scoped `auth_account_lookup(email)` or `auth_session_lookup(token_hash)` SECURITY DEFINER functions. The runtime connection remains `NOBYPASSRLS`, then sets `app.tenant_id` before tenant-scoped work.
- `users.email` remains globally unique for the pilot. This is intentional; tenant-local email reuse is deferred.
- Migrations now copy before sort, reject duplicate versions, run in a transaction abstraction, acquire `pg_advisory_xact_lock` inside that transaction, and roll back on failure.
- Session validity checks both token hash and strict pre-expiry time. CSRF stores a hash and compares its derived value in constant time.
- HTML SMTP body uses quoted-printable; hostless HTTP(S) URLs are rejected. Schedules and retries have expanded edge-case coverage.
- Delivery claim creates a unique `sending` delivery. Finalization validates 1–3 attempts; success conditionally updates the delivery window and schedule `last_success_at` in one transaction, while failure records status, attempt count, and error without advancing the schedule.

### Fix RED evidence

`go test ./internal/auth ./internal/digest`

Output: `Session.ValidAt` and `HashCSRFToken` were undefined; MIME lacked `Content-Transfer-Encoding`; hostless `https:///...` was accepted.

`go test ./internal/store -run TestInitialSchemaLeavesSessionTenantAndRLSOutOfAuthenticationLookup`

Output: `FAIL ... sessions must derive tenancy through users only`.

`go test ./internal/store -run TestApplyMigrations`

Output: `MigrationTx` was undefined and the old runner accepted a non-transactional executor.

`go test ./internal/store -run 'TestDeliveryClaim|TestFinalize'`

Output: `DeliveryTx`, `DeliveryRepository`, and `DeliveryClaim` were undefined.

`go test ./internal/store -run TestInitialSchemaKeepsTenantReferencesInTenant`

Output: missing `sending` delivery state SQL contract.

`go test ./internal/store -run TestFinalizeFailureRejectsAttemptCountsOutsideRetryLimit`

Output: invalid attempt count `4` was written instead of rejected.

### Fix GREEN evidence

`go test ./internal/auth ./internal/digest ./internal/store`

Output: `ok namo/internal/auth`, `ok namo/internal/digest`, `ok namo/internal/store`.

`go vet ./internal/auth ./internal/digest ./internal/store`

Output: exit `0` with no findings.

## Fix round 2 — release blockers

- `tenants` now has enforced RLS and only the current tenant policy. Regular platform-admin requests do not gain a cross-tenant table policy; tenant-wide administration needs a separately reviewed narrow database operation.
- The two authentication lookups are SECURITY DEFINER, use `pg_catalog` only as their search path, fully qualify public tables, revoke PUBLIC execution, and grant only `namo_runtime`. Serving connections use `namo_runtime` `NOBYPASSRLS` permissions (or a login role granted only that role), never a bypass pool.
- `PgxMigrationBeginner` and `PgxDeliveryBeginner` adapt the shared `Begin(context.Context) (pgx.Tx, error)` shape used by pgx connections and pgx pools while retaining fakeable local transaction interfaces.
- Delivery success finalization conditionally advances a schedule only when every enabled recipient is `sent` for the same due window. The delivery row and conditional schedule-window update share one transaction. A failure is retryable on a later run and never advances the marker; the marker uses `due_at`, not transmission time.

### Round 2 RED evidence

`go test ./internal/store`

Output: finalization tests failed with too many arguments because the old API had no due-window completion input; SQL-contract expectations for tenant RLS and authentication bootstrap were absent before the migration update.

`go test ./internal/store -run TestPgxAdaptersDelegateBeginErrors`

Output: `PgxTxStarter`, `PgxMigrationBeginner`, and `PgxDeliveryBeginner` were undefined before adapter implementation. A temporary direct `pgxpool` test import also reported its missing transitive `go.sum` entry; it was removed without changing dependency files.

### Round 2 GREEN evidence

`go test ./internal/auth ./internal/digest ./internal/store`

Output: `ok namo/internal/auth`, `ok namo/internal/digest`, `ok namo/internal/store`.

`go vet ./internal/auth ./internal/digest ./internal/store`

Output: exit `0` with no findings.

### Remaining live verification

No PostgreSQL instance is installed. SECURITY DEFINER ownership/privileges, the `NOBYPASSRLS` serve role, forced tenant RLS, advisory-lock behavior, and multi-recipient SQL execution remain live-unverified. SMTP transport/TLS delivery also remains unverified.

## Fix round 3 — final focused pass

- Every migration run now creates or hardens `namo_runtime` with `ALTER ROLE ... NOLOGIN NOBYPASSRLS`; an existing unsafe role is no longer silently accepted.
- `namo_auth_definer` is `NOLOGIN BYPASSRLS NOINHERIT`, receives only schema usage and column-level `SELECT` on `users` and `sessions`, and owns the two auth functions. `auth_account_lookup` returns only ID, tenant, email, bcrypt hash, and role; `auth_session_lookup` returns only ID, tenant, email, role, and an exact unexpired session expiry.
- Runtime grants exclude `sessions` and migration tables. They are limited to public schema usage plus the RLS-protected application tables; all sequence privileges are revoked because UUID defaults require no runtime sequence access.
- Failed deliveries no longer call schedule completion. A failed row can be reclaimed only by its same idempotency key, reset to one attempt for the next run, and retried up to `MaxDeliveryAttempts` (3) within that run. A `sent` row cannot be reclaimed.

### Round 3 RED evidence

`go test ./internal/store`

Output: the old claim used `ON CONFLICT DO NOTHING`; failure finalization issued a second schedule update; the schema lacked `ALTER ROLE namo_runtime NOLOGIN NOBYPASSRLS` and the narrow definer contracts.

### Round 3 GREEN evidence

`go test ./internal/auth ./internal/digest ./internal/store`

Output: `ok namo/internal/auth`, `ok namo/internal/digest`, `ok namo/internal/store`.

`go vet ./internal/auth ./internal/digest ./internal/store`

Output: exit `0` with no findings.

### Migration-owner assumptions

`migrate` uses the separate migration URL and a credential allowed to create/alter the two NOLOGIN roles, grant privileges, and transfer function ownership. `serve` uses only a login role with `NOBYPASSRLS` and membership/privileges equivalent to `namo_runtime`; it never receives migration-owner credentials. PostgreSQL RLS, role ownership, grants, and function execution remain live-unverified because no server is installed. The pgxpool transitive checksum is now present; the adapter uses the shared pgx `Begin` signature and remains pgxpool-compatible.

## Fix round 4 — session privilege boundary

- Both existing roles are hardened on every migration: no login, superuser, database, role, replication, bypass (runtime only), or inherited membership. Safe `pg_catalog` loops revoke every parent-role membership before grants are applied.
- `auth_session_create` and `auth_session_delete` are SECURITY DEFINER functions with `pg_catalog` search paths and fully-qualified `public.sessions`. They reject empty hashes; create also rejects null, non-future, or over-90-day expiry. PUBLIC is revoked and only `namo_runtime` can execute them.
- The definer role receives exactly the required `sessions` column SELECT/INSERT and table DELETE privileges. Runtime direct `sessions` access remains absent.

### Round 4 RED evidence

`go test ./internal/store -run TestInitialSchemaKeepsTenantReferencesInTenant`

Output: missing full runtime-role hardening contract before the migration update.

### Round 4 GREEN evidence

`go test ./internal/auth ./internal/digest ./internal/store`

Output: `ok namo/internal/auth`, `ok namo/internal/digest`, `ok namo/internal/store`.

`go vet ./internal/auth ./internal/digest ./internal/store`

Output: exit `0` with no findings.
