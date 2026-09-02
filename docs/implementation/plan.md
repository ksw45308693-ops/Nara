# namo 구현 작업

## Global constraints

- Free and open-source dependencies only. No paid API, service, or plugin.
- No Git operation.
- Go 1.27 module, FreeBSD amd64 production target, PostgreSQL storage.
- Server-rendered HTML with plain CSS and minimal JavaScript.
- Test-first development for behavior. Keep agent-owned paths disjoint during parallel work.

## Subagent allocation and gates

| Track | Ownership | Completion gate |
| --- | --- | --- |
| Procurement and matching | Domain model, official API paging/retry, normalization, matching reasons | Fixture tests, quota guards, independent review |
| Identity and PostgreSQL | Roles, sessions, RLS schema, migrations | Security tests, SQL contract tests, independent review |
| Digest delivery | Fixed delivery window, recipient/item snapshot, retry fencing | Crash-safe three-attempt limit, duplicate-send review |
| Web UI | Server-rendered screens, responsive CSS, minimal JavaScript | HTTP/CSRF/role tests and four-viewport browser QA |
| Tenant onboarding | Tenant creation, invitation mail, single-use acceptance | Anonymous CSRF, token-hash and concurrent-use review |
| Runtime and deployment | CLI, scheduler, FreeBSD/Nginx operations | Full regression, vet, cross-build, operations checklist |

Only free components are permitted: Go standard tooling, PostgreSQL, pgx,
Nginx, the public data.go.kr API, and an existing or self-hosted SMTP relay.
No paid plugin, hosted queue, billing service, or proprietary runtime is used.

## Task 1: Procurement and matching

Own `internal/model`, `internal/procurement`, and `internal/matcher`. Implement normalized domain types, official JSON API paging and error parsing, retry, notice identity and revision hashing, Unicode-aware practical filters, match reasons, and tests with sanitized fixtures. Do not edit `go.mod` or other paths.

## Task 2: Identity, tenant storage, and digest

Own `internal/auth`, `internal/store`, `internal/digest`, and `migrations`. Implement roles, bcrypt passwords, opaque sessions, CSRF primitives, tenant-aware schema with row-level security, SMTP HTML digest, schedule calculation, retry and delivery idempotency, and tests. Do not edit `go.mod` or other paths.

## Task 3: Web interface

Own `internal/web` and `web`. Implement an embeddable server-rendered UI matching `DESIGN.md`: login, dashboard, notices, filters, notifications, settings, and platform administration. Use sample view models, semantic HTML, responsive CSS, minimal JavaScript, and handler/template tests. Do not edit `go.mod` or other paths.

## Task 4: Integration

Compose the root CLI, PostgreSQL repositories, web handlers, collector, matcher, scheduler, and mailer with `internal/app`. Add health endpoints, runtime logging, embedded assets, and integration tests.

## Task 5: Deployment and verification

Add FreeBSD `rc.d`, Nginx, log rotation, backup/restore, environment example, and concise operations documentation. Run full tests, vet, race checks, browser inspection, the UI detector, and FreeBSD amd64 cross-build.

## Task 6: Tenant onboarding hardening

Use a separate migration and service boundary for platform-created tenants,
tenant-admin member invitations, hashed 48-hour tokens, anonymous CSRF, and
single-use acceptance. Keep raw bearer tokens in the invitation URL only.
