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
- A FreeBSD cross-build proves compilation only. API, Nginx, `rc.d`, report ownership,
  browser download, backup restore and FreeBSD runtime checks remain real-server acceptance work.

## Task 11 local acceptance evidence — 2026-09-02

- Windows `go test -count=1 ./...`: PASS for all packages
- Windows `go vet ./...`: PASS
- `go mod verify`: PASS (`all modules verified`)
- Changed-file `gofmt`: PASS (no output)
- `go list -f`: `name=main import=namo`; 설치 대상은 로컬 `<GOBIN>/namo.exe`
- Root `go install .`: PASS
- Installed binary without arguments: printed
  `사용법: namo <serve|migrate|create-admin|collect-once|generate-report>` and exited 2
- FreeBSD artifact: `build/namo-freebsd-amd64`, 21,920,119 bytes
- FreeBSD SHA-256: `11B12EC9A934310528DE6C7CBCB72D3A5B85AC1DC8C3C0A59DCD5EEE2CB37939`
- WSL/Linux Go 1.27 with GCC, `CGO_ENABLED=1 go test -race -count=1 ./...`: PASS for all packages
- WSL `go vet ./...`: PASS
- WSL `sh -n` for all three FreeBSD scripts: PASS
- WSL `sh deploy/freebsd/freebsd_scripts_test.sh`: PASS (`freebsd scripts: PASS`)
- Legacy name scan: no previous hyphenated or underscored product-name pattern outside `build` and `.git`
- Task 9 independent review after fixes: ADDRESSED, Critical 0, Important 0, Minor 0
- Task 10 Fix round 3 final review: ADDRESSED, Critical 0, Important 0, Minor 0

Local browser sample QA passed at 981×714, 1440×900, 1024×768, and
390×844. It found no horizontal overflow, text clipping in inspected elements,
console warning, or console error. Mobile `inert`, focus trap, Escape, focus return, and skip-link
behavior passed. Sampled text contrast was 4.88:1–13.72:1. The detector lacked
its parser modules and used a regex fallback with 0 findings; this is not a
detector clean result.

Verbose PostgreSQL tests explicitly skipped
`TestPostgresReleaseContracts`,
`TestPostgresReportRepositoryClaimsFencesSnapshotsAndIsolatesTenants`, and
`TestPostgresReportSchema` because `TEST_POSTGRES_OWNER_URL` and
`TEST_POSTGRES_RUNTIME_URL` were absent. The command suite passed, but these
tests did not run against PostgreSQL. `G2B_API_KEY` and `DATABASE_URL` were also
absent, so no actual API, database, or SMTP integration ran.

The local Task 11 phase is complete. Overall Task 11 remains pending explicit
deployment approval and nara Jail acceptance. No FreeBSD or Jail runtime,
service, Nginx, live database, backup restore, or deployed-browser acceptance
ran. No remote state or Git state changed. See
`docs/implementation/reports/report-delivery-acceptance.md` for the acceptance
boundary.

Final cross-task review fixes are included: an empty manual run now reports that
no file was created, the report-schema integration test accepts later migrations
while preserving the 0007 baseline, and acceptance evidence redacts the local
absolute install path. Fresh Windows full tests/vet/module checks, WSL full race
and vet, FreeBSD shell mocks, and the final FreeBSD cross-build passed afterward.

## Report delivery Task 10 local evidence — 2026-09-02

- The focused contract test failed first on the missing `REPORT_DIR`, backup-state,
  report-archive, and report-operations contracts.
- `go test -count=1 ./internal/config`: PASS
- `go test -count=1 ./...`: PASS
- `go vet ./...`: PASS
- `go mod verify`: PASS (`all modules verified`)
- `gofmt -l internal/config/freebsd_contract_test.go`: PASS (no output)
- WSL `sh -n deploy/freebsd/namo.in deploy/freebsd/backup-namo.sh`: PASS

The local checks do not prove service stop/start recovery, FreeBSD ownership and
modes, tar contents, authenticated browser download, PostgreSQL restore, or Jail runtime behavior.
The repository-wide `gofmt -l .` command still reports Go files outside Task 10;
this task did not rewrite those files.

### Fix round 1

- RED: the focused contract and shell test rejected the old root-equivalent path,
  component-symlink, fixed report path, unhashed manifest, and direct restore behavior.
- A second RED proved that the SHA-256 mock did not identify the three hashed files.
- Windows `go test -count=1 ./internal/config` and `go test -count=1 ./...`: PASS
- WSL `go test -count=1 ./internal/config` and `go test -count=1 ./...`: PASS
- `go vet ./...` and `go mod verify`: PASS
- `gofmt -l internal/config/freebsd_contract_test.go`: PASS (no output)
- WSL `sh -n` for all three FreeBSD scripts: PASS
- WSL execution test: PASS for running and stopped services on backup success
  and failure. It also checks the three SHA-256 input paths.
- All 12 `sh` blocks in `docs/operations-freebsd.md`: WSL syntax PASS

These checks use command mocks. Live FreeBSD service recovery, root ownership,
real PostgreSQL dumps, tar restore, report hash comparison, and paired restore remain acceptance work.

### Fix round 2

- RED: the focused test rejected the old restore procedure because it did not keep
  the restored database and staged reports in one verification environment. It
  also required a whitespace-aware Nginx static-directive guard, a no-argument-only
  rc.d test seam, and duplicate `relative_path` rejection.
- A second RED exposed that the isolated PostgreSQL socket parent was not
  traversable by the `namo` verification user.
- The restore guide now verifies the restored DB and staged `REPORT_DIR` together
  in an isolated Jail. It does not publish either part to the live service.
- Windows and WSL focused/full Go tests: PASS
- `go vet ./...`, `go mod verify`, and focused `gofmt -l`: PASS
- WSL syntax for all three FreeBSD scripts and all 10 operations shell blocks: PASS
- WSL service-state and security execution test: PASS

No live database, report directory, service, Nginx process, Jail, or server was changed.
The isolated restore procedure still requires FreeBSD/PostgreSQL acceptance testing.

### Fix round 3

- RED: the focused contract rejected the `restore_admin` verification DSN,
  sourced verification environment, and missing clean environment launch.
- The verification command now connects as the restored `namo_app` runtime role.
- `/usr/bin/env -i` passes only `PATH`, `DATABASE_URL`, `DELIVERY_MODE`,
  `REPORT_DIR`, and `SESSION_KEY` to `generate-report`.
- The temporary verification environment file and its cleanup were removed.
- Focused contracts and all 10 operations shell blocks: PASS

No live database, report directory, service, Jail, or server was changed.

The independent Fix round 3 final review was ADDRESSED with Critical 0,
Important 0, and Minor 0.
