# Implementation ledger — plan: docs/implementation/plan.md

## Preflight

| Work pair | Shared contract | Ruling |
| --- | --- | --- |
| Procurement ↔ Identity/storage | `internal/model` domain records | Procurement owns model definitions; storage consumes them only during integration. |
| Procurement ↔ Web | Notice and match view data | Web uses local view models first; integration maps domain values later. |
| Identity/storage ↔ Web | User, role, tenant, CSRF | Web uses local view models and form contracts; integration binds auth middleware later. |
| All tracks ↔ `go.mod` | External dependencies | Parallel agents do not edit `go.mod`; the controller resolves free dependencies once. |

Ruling: Go 1.27 is the module target. Final local verification used `go1.27.0 windows/amd64` with `GOTOOLCHAIN=auto`.

Ruling: The user-supplied image is binding visual authority, so no alternative visual-direction round is required.

Ruling: The subagent skill normally assumes Git commits and sequential implementers. This workspace forbids Git, and the user explicitly requested parallel agents. Disjoint ownership paths and controller integration replace commit-based isolation.

Ruling: The skills.sh search found Go and database skills, but no new skill is installed. Existing local TDD, ponytail, Impeccable, browser-control, review, and verification skills cover the work without adding software or service cost.

Controller foundation: `internal/config` and `internal/cli` completed test-first. Focused tests passed locally with the free Go 1.27 toolchain.

## Subagent build rounds

| Round | Assigned direction | Integrated result |
| --- | --- | --- |
| 1 | Procurement/matching | Four notice categories, paging/retry, normalization, quarantine warnings, Unicode-aware rules and match reasons |
| 1 | Identity/storage/digest | bcrypt/session/CSRF primitives, tenant RLS migrations, schedules, fixed delivery windows and SMTP message generation |
| 1 | Server-rendered web UI | Login, dashboard, notices, filters, notifications, settings and platform administration matching the supplied visual system |
| 2 | Application integration | CLI commands, PostgreSQL repositories, collection/digest scheduler, onboarding and health endpoints |
| 2 | FreeBSD operations | static cross-build target, `rc.d`, Nginx, newsyslog, backup/restore and zero-cost operating guide |
| 3 | Independent release review | API budget/redaction, migration upgrade safety, invitation locking, recipient identity, filter revision fencing and asynchronous administrator collection |
| 4 | Final boundary review | Logout, Seoul invitation time, request-wide mail budget, unknown migration rejection and atomic backup completion marker |

Each subagent owned a bounded track. The controller integrated shared contracts,
ran focused regressions, and reassigned review findings to a different track.
No Git command, paid plugin, hosted queue, paid mail API, or proprietary UI kit
was used.

## Current verification boundary

- Fixture/unit/HTTP/SQL-contract tests cover the application without live credentials.
- Browser QA covers 981×714, 1440×900, 1024×768 and a 390×844 responsive viewport.
- PostgreSQL integration tests are opt-in through `TEST_POSTGRES_OWNER_URL` and
  `TEST_POSTGRES_RUNTIME_URL`; they skip when a live test database is unavailable.
- A FreeBSD cross-build proves compilation only. API, SMTP, Nginx, `rc.d`,
  PostgreSQL restore and FreeBSD runtime checks remain real-server acceptance work.

## Final local release evidence — 2026-09-01

- `go mod verify`: PASS (`all modules verified`)
- `go test -count=1 ./...`: PASS
- `go vet ./...`: PASS
- Independent final review: Blocker 0, High 0, Medium 0
- `CGO_ENABLED=0` amd64 builds: Windows, Linux, macOS and FreeBSD PASS
- FreeBSD artifact: `build/g2b-monitor-freebsd-amd64`
- FreeBSD SHA-256: `06C571111C4F8CDF7944209421776387880F8BEF432C96A36933974FA603BD1A`
- UI browser QA: 981×714, 1440×900, 1024×768 and 390×844 PASS

Local limits: PostgreSQL integration was explicitly skipped because its two test
URLs were absent. `go test -race` could not run because this Windows host has no
C compiler. A POSIX shell was also unavailable for local `sh -n`; the backup and
FreeBSD configuration contracts passed Go tests, while actual FreeBSD execution
remains part of server acceptance.
