# 고객사별 HTML 리포트 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 고객사별 예약 시각에 신규 매칭 공고를 하나의 HTML 리포트로 안전하게 저장하고, 웹과 CLI에서 수동 생성·조회·다운로드할 수 있게 한다.

**Architecture:** 기존 메일용 `DigestRunner`와 SMTP 코드는 보존하되 실행 경로에서는 분리한다. 신규 `ReportRunner`가 PostgreSQL 스냅샷, 표준 라이브러리 HTML 렌더러, `os.OpenRoot` 기반 파일 저장소를 조합한다. 운영 모드는 `DELIVERY_MODE=report`만 허용한다.

**Tech Stack:** Go 1.27 표준 라이브러리, pgx v5, PostgreSQL RLS, 서버 렌더링 HTML/CSS, 최소 JavaScript, FreeBSD rc.d/Nginx.

**Spec:** `docs/implementation/report-delivery-design.md`

## Global Constraints

- 유료 서비스·플러그인·외부 스토리지를 추가하지 않는다.
- Git 명령과 push/pull/commit을 수행하지 않는다.
- `migrations/0001_initial.sql`부터 `0006_expired_invitation_upgrade.sql`까지 기존 파일 바이트를 바꾸지 않는다.
- `DigestRunner`, `SMTPMailer`, 메일 테스트는 삭제하지 않는다. 운영 연결만 끊는다.
- 공고가 없는 예약 구간과 빈 수동 요청은 DB 리포트 행과 파일을 만들지 않는다.
- 테넌트가 입력한 이름은 파일 경로에 사용하지 않는다.
- 모든 변경은 실패 테스트 → 최소 구현 → 관련 회귀 테스트 순서로 진행한다.
- 전체 회귀, vet, FreeBSD 교차 빌드, Jail 실검증을 통과하기 전 완료로 표시하지 않는다.

## 서브 에이전트 배정

| 단계 | 담당 | 소유 파일 | 검토 게이트 |
|---|---|---|---|
| A1 | 운영 에이전트 | `.gitattributes`, FreeBSD 배포 계약 | 루트가 기존 마이그레이션 의미·체크섬 검토 |
| A2 | 데이터 에이전트 | `migrations/0007_*`, PostgreSQL 리포트 저장소 | RLS·동시 claim 독립 검토 |
| A3 | 리포트 코어 에이전트 | `internal/report` | 경로 이탈·HTML escaping 독립 검토 |
| B1 | 실행 에이전트 | runner, job, scheduler, config, CLI, runtime | 메일 비활성·3회 제한 독립 검토 |
| B2 | UI 에이전트 | `internal/web`, `postgres_web`, `web/*`, 초대 링크 | 권한·CSRF·테넌트 격리 독립 검토 |
| B3 | 운영 에이전트 | rc.d, 백업, 운영 문서 | FreeBSD 구문·권한·복구 검토 |
| C | 루트 에이전트 | 통합 충돌 해소·전체 검증·Jail 점검 | 증거 확인 후 완료 판정 |

동시에 실행할 때는 소유 파일이 겹치지 않는 A1/A2/A3만 병렬로 진행한다. B 단계는 A 단계의 타입과 SQL 계약이 확정된 뒤 진행한다. 같은 파일을 이어서 수정하는 Task 2·5와 Task 8·9는 각각 동일한 데이터/UI 에이전트가 순차 수행한다. 나머지는 작업별 구현 에이전트를 배정하고, 루트 또는 별도 검토 에이전트가 테스트 결과와 diff를 확인한다.

## 승인 설계의 구현상 보완

이번 계획 승인은 다음 내부 필드와 동작 보완도 포함한다.

- `report_items`: 재시작·재시도에도 동일한 HTML을 만들기 위한 고정 출력 스냅샷이다.
- `claim_token`, `claimed_at`, `window_start_at`, `window_end_at`, `created_at`: 동시 실행 차단, stale lease 복구, 실제 대상 구간 표시에 필요하다.
- 관리자 재시도: 3회 실패한 기존 report 행의 attempts를 새 작업 실행 기준으로 초기화하고 같은 report ID·상대 경로·고정 항목을 다시 사용한다.

이 필드들은 외부 공개 API가 아니라 PostgreSQL 내부 복구 계약이다.

---

## Task 1: OS 간 소스 바이트 계약 고정

**Files:**

- Modify: `.gitattributes`
- Modify: `migrations/embed.go`
- Create: `migrations/line_endings_test.go`
- Normalize only: `deploy/freebsd/namo.in`, `deploy/freebsd/*.sh`, `deploy/freebsd/*.conf`, `.env.example`
- Modify: `internal/store/onboarding_upgrade_schema_contract_test.go`
- Modify: `internal/store/onboarding_expiry_upgrade_schema_contract_test.go`

- [ ] `.gitattributes`에 다음 계약을 추가한다.

```gitattributes
*.sql text eol=lf
*.sh text eol=lf
deploy/freebsd/namo.in text eol=lf
deploy/freebsd/*.conf text eol=lf
.env.example text eol=lf
```

- [ ] `migrations/line_endings_test.go`에 `All()`이 반환하는 모든 SQL이 `\r`을 포함하지 않는 실패 테스트를 먼저 추가한다.
- [ ] 기존 `0001`~`0006` 파일 바이트는 바꾸지 않고 `migrations.All()`이 읽은 직후 CRLF를 LF로 정규화한다. FreeBSD의 기존 LF 소스 결과는 변하지 않아야 한다.
- [ ] `0005`, `0006`을 직접 읽는 두 계약 테스트도 assertion 전에 읽은 문자열만 CRLF→LF로 정규화한다. SQL 파일 자체는 수정하지 않는다.
- [ ] checksum 대상이 아닌 FreeBSD shell/config와 `.env.example`은 실제 파일 줄바꿈도 LF로 통일한다.
- [ ] 기존 Windows 실패였던 초대 마이그레이션 계약 테스트가 통과하는지 확인한다.
- [ ] nara Jail의 `schema_migrations` 체크섬과 정규화된 1~6 checksum이 일치하는지 migration 실행 전에 읽기 전용으로 확인한다. 다르면 ledger를 자동 수정하지 않고 중단한다.

**Verify:**

```powershell
go test -count=1 ./migrations ./internal/store
```

## Task 2: 리포트 스키마와 RLS 추가

**Files:**

- Create: `migrations/0007_report_delivery.sql`
- Modify: `migrations/embed_test.go`
- Create: `internal/app/postgres_report_schema_integration_test.go`
- Modify: `internal/app/postgres_release_integration_test.go`

- [ ] 마이그레이션 개수 7, 순서 1~7, 신규 RLS·부분 고유키·복합 FK를 요구하는 실패 테스트를 작성한다.
- [ ] `reports`를 다음 공개 필드와 내부 복구 필드로 만든다.

```text
id, tenant_id, schedule_id, due_at, window_start_at, window_end_at,
trigger, status, relative_path, sha256, notice_count,
attempts, last_error, claim_token, claimed_at,
generated_at, created_at
```

- [ ] `trigger`는 `scheduled|manual`, `status`는 `generating|generated|failed`, `attempts`는 0~3으로 제한한다.
- [ ] 예약 행에만 `(tenant_id, schedule_id, due_at)` 부분 고유 인덱스를 적용하고, 예약 행은 `digest_windows` 복합 키를 참조한다. 수동 행은 `schedule_id IS NULL`이어야 한다.
- [ ] `report_items`에 `report_id`, `tenant_id`, 고정 순서, 공고/매칭 ID, 제목, 업무구분, 기관, 지역, 금액, 마감, 원문 URL, 규칙명·매칭 사유를 저장한다. 재시도 때 실시간 공고를 다시 읽지 않는다.
- [ ] 두 테이블 모두 RLS와 FORCE RLS를 적용하고 `namo_runtime`에는 필요한 SELECT/INSERT/UPDATE만 부여한다.
- [ ] PostgreSQL 통합 테스트로 동일 예약 중복 차단, 수동 복수 생성, 타 테넌트 SELECT/UPDATE 차단, 예약 FK, 3회 초과 거부를 검증한다.

**Verify:**

```powershell
go test -count=1 ./migrations
if (-not $env:TEST_POSTGRES_OWNER_URL) { throw 'TEST_POSTGRES_OWNER_URL is required' }
if (-not $env:TEST_POSTGRES_RUNTIME_URL) { throw 'TEST_POSTGRES_RUNTIME_URL is required' }
go test -count=1 ./internal/app -run 'TestPostgresReport|TestPostgresRelease'
```

실제 URL이 없으면 PostgreSQL 통합 테스트는 미검증으로 기록하며 통과로 간주하지 않는다.

## Task 3: 자체 포함 HTML 렌더러 구현

**Files:**

- Create: `internal/report/render.go`
- Create: `internal/report/render_test.go`

- [ ] 다음 순수 타입과 함수를 테스트에서 먼저 고정한다.

```go
type Match struct {
    RuleName string
    Reasons  []string
}

type Notice struct {
    ID, Title, Category, Agency, Region, SourceURL string
    Amount                                        int64
    Deadline                                      time.Time
    Matches                                       []Match
}

type Document struct {
    TenantName, ScheduleName string
    Trigger                  string
    DueAt, WindowEnd         time.Time
    WindowStart              *time.Time
    Notices                  []Notice
}

func BuildHTML(Document) ([]byte, error)
```

- [ ] 한국어 제목, 고객사·실제 매칭 구간, 예약/스냅샷 시각, 전체/업무구분별 건수, 공고 제목·기관·지역·금액·마감·매칭 이유·원문 링크를 한 HTML 파일에 렌더링한다. 최초 실행처럼 하한이 없으면 `최초 수집 이후`로 표시한다.
- [ ] 외부 폰트·CSS·JavaScript·이미지를 참조하지 않고 이미지 기준 네이비·시안·연회색·녹색 토큰을 `<style>`에 포함한다.
- [ ] 사용자/공고 문자열을 `html/template`로 이스케이프하고 원문 링크는 HTTP(S)만 활성화한다. 그 외 스킴은 텍스트로 표시한다.
- [ ] 입력 순서와 `DueAt`만으로 바이트가 결정되게 하여 재시도 시 SHA-256이 같도록 한다.
- [ ] escaping, 업무구분 집계, 빈 선택 필드, 잘못된 URL, 동일 입력 결정성 테스트를 통과시킨다.

**Verify:**

```powershell
go test -count=1 ./internal/report -run TestBuildHTML
```

## Task 4: 제한 루트 기반 원자 파일 저장 구현

**Files:**

- Create: `internal/report/files.go`
- Create: `internal/report/files_test.go`

- [ ] 다음 경계를 실패 테스트로 먼저 고정한다.

```go
type FileResult struct {
    RelativePath string
    SHA256       string
}

type FileStore struct { root *os.Root }

func OpenFileStore(root string) (*FileStore, error)
func (s *FileStore) Write(ctx context.Context, relativePath string, body []byte) (FileResult, error)
func (s *FileStore) Open(relativePath string) (*os.File, os.FileInfo, error)
func (s *FileStore) Close() error
```

- [ ] `REPORT_DIR`은 절대 경로이며 볼륨 루트가 아니어야 한다. 상대 경로, `..`, 절대 하위 경로, NUL, 루트 밖 심볼릭 링크를 거부한다.
- [ ] `os.OpenRoot` 아래에서 `MkdirAll(0750)` → 임시 파일 `O_CREATE|O_EXCL` → write → `Sync` → `Chmod(0640)` → close → `Root.Rename` 순서로 저장한다.
- [ ] 기존 최종 파일은 SHA-256이 같으면 성공 복구로 처리하고, 다르면 덮어쓰지 않고 충돌 오류를 반환한다.
- [ ] 모든 실패 경로에서 임시 파일을 제거한다. 코어에는 FreeBSD 전용 `chown`을 넣지 않는다.
- [ ] 경로 이탈, 외부 심볼릭 링크, 임시 파일 정리, 동일/상이한 해시, 원자 rename, Unix 0640 권한을 테스트한다. Windows에서는 POSIX 권한 assertion만 건너뛴다.

**Verify:**

```powershell
go test -count=1 ./internal/report -run 'TestFileStore'
```

## Task 5: PostgreSQL 예약·수동 작업 저장소 구현

**Files:**

- Create: `internal/app/postgres_report.go`
- Create: `internal/app/postgres_report_test.go`
- Create: `internal/app/postgres_report_repository_integration_test.go`
- Create: `internal/app/report_contract.go`

- [ ] `internal/app/report_contract.go`에 먼저 다음 계약 타입을 선언하고 저장소 테스트를 작성한다.

```go
type ReportWork struct {
    ReportID, TenantID, TenantName, ScheduleID, ScheduleName string
    Trigger, RelativePath, ClaimToken                        string
    DueAt, WindowEnd                                        time.Time
    WindowStart                                             *time.Time
    Attempts                                                int
    Notices                                                 []report.Notice
}

type ReportRepository interface {
    ClaimDueReports(context.Context, time.Time) ([]ReportWork, error)
    ReclaimReport(context.Context, string, string) (ReportWork, bool, error)
    ClaimManualReport(context.Context, string, time.Time) (ReportWork, bool, error)
    RetryReport(context.Context, string, string, time.Time) (ReportWork, bool, error)
    FinalizeReport(context.Context, ReportWork, ReportArtifact, time.Time) error
    FinalizeReportFailure(context.Context, ReportWork, error) error
}

type ReportArtifact struct {
    RelativePath string
    SHA256       string
    NoticeCount  int
}

type ReportWriter interface {
    Write(context.Context, string, []byte) (report.FileResult, error)
}

type ReportOutcome struct {
    ID, RelativePath string
    Created          bool
    NoticeCount      int
}
```

- [ ] `ClaimDueReports`는 스케줄 잠금, `digest_windows` 생성, 기존 window item 스냅샷, `reports/report_items` 생성, 1차 fencing claim을 한 트랜잭션에서 처리한다.
- [ ] 수신자가 없어도 예약 리포트를 만들 수 있어야 하며 기존 `buildDigestWorks`는 재사용하지 않는다.
- [ ] 매칭이 없으면 파일과 report 행 없이 window 완료와 `schedules.last_success_at` 갱신만 수행한다.
- [ ] 활성 `generating` lease는 건너뛰고, 15분 지난 lease 또는 `failed AND attempts < 3`만 새 `claim_token`으로 재선점한다.
- [ ] `ClaimManualReport`는 현재 열린 매칭을 `report_items`에 고정하지만 스케줄 성공 시각을 갱신하지 않는다. 매칭이 없으면 `created=false`를 반환한다.
- [ ] `RetryReport`는 같은 테넌트의 `failed` 기존 행만 선택하고 report ID·상대 경로·report_items를 유지한 채 attempts를 새 3회 작업 기준으로 초기화하고 새 fencing token을 발급한다.
- [ ] finalize는 claim token과 attempts를 WHERE 조건에 넣어 오래된 작업자의 성공/실패 기록을 차단한다.
- [ ] 성공 finalize 한 트랜잭션에서 report generated, digest window completed, schedule last success를 갱신한다. 수동은 report만 갱신한다.
- [ ] SQL 단위 테스트 후 실제 PostgreSQL에서 동시 claim, stale lease, fencing, 3회 제한, 빈 구간, 수동 스냅샷과 RLS를 검증한다.

**Verify:**

```powershell
go test -count=1 ./internal/app -run 'TestPostgresReport'
```

## Task 6: ReportRunner와 작업 저널 구현

**Files:**

- Create: `internal/app/report_runner.go`
- Create: `internal/app/report_runner_test.go`
- Create: `internal/app/report_job.go`
- Create: `internal/app/report_job_test.go`
- Create: `internal/app/postgres_report_runs.go`
- Create: `internal/app/postgres_report_runs_test.go`
- Modify: `internal/jobs/scheduler.go`
- Modify: `internal/jobs/scheduler_test.go`

- [ ] Task 5에서 고정한 `ReportRepository`, `ReportWriter`, `ReportOutcome` 계약의 fake를 먼저 만들고 runner 실패 테스트를 작성한다.
- [ ] Runner는 claim된 고정 항목을 렌더링하고 deterministic 상대 경로에 저장한 뒤 DB를 finalize한다.
- [ ] 같은 실행에서 최대 3회까지 `ReclaimReport`하며, 3회 후 failed 상태를 유지하고 다음 자동 실행에서 다시 시도하지 않는다.
- [ ] 파일 저장 뒤 DB finalize가 실패하면 stale reclaim 시 같은 파일 SHA를 확인해 완료할 수 있어야 한다.
- [ ] 예약 파일명은 `<tenant-uuid>/<yyyy>/<mm>/namo-<yyyymmdd-HHMMSS>.html`, 수동 파일명은 마지막에 report UUID를 추가한다.
- [ ] `job_runs.kind='report'`로 시작/완료/건너뜀/실패를 기록하고 고객사별 결과를 남긴다.
- [ ] 스케줄러의 실행 필드를 report로 바꾸고 collection 완료 시각 다음에 report job을 실행한다. `ErrReport`를 추가하되 `ErrDigest`는 같은 sentinel의 호환 별칭으로 남겨 기존 호출자의 `errors.Is` 계약을 유지한다.
- [ ] 성공, 빈 구간, 중복, writer 실패, finalize 실패, 3회 제한, 컨텍스트 취소, 수동 생성 테스트를 통과시킨다.
- [ ] 관리자 재시도는 새 수동 report를 만들지 않고 기존 report ID·경로·고정 항목을 사용하며 다시 최대 3회만 시도하는지 테스트한다.

**Verify:**

```powershell
go test -count=1 ./internal/app -run 'TestReport|Test.*ReportJob'
go test -count=1 ./internal/jobs
```

## Task 7: 설정·CLI·런타임을 report 모드로 전환

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `internal/app/runtime.go`
- Modify: `internal/app/runtime_test.go`
- Modify: `.env.example`
- Modify: `README.md`

- [ ] `Config`에 `DeliveryMode`, `ReportDir`을 추가하고 기본 활성 모드를 `report`로 고정한다.
- [ ] `serve`와 `generate-report`는 `DELIVERY_MODE=report`와 안전한 절대 `REPORT_DIR`을 요구한다. `serve`에서는 SMTP 필수 검증을 제거한다.
- [ ] 공개 사용법을 `namo <serve|migrate|create-admin|collect-once|generate-report>`로 변경한다.
- [ ] `send-test-mail`은 명령 맵과 기존 코드는 남기되 사용법에서 숨긴다. `cli.Run`이 설정 로드 전에 차단하고 runtime 직접 호출도 `메일 기능은 현재 비활성화되어 있습니다` 오류를 반환한다.
- [ ] `generate-report --tenant <uuid>`를 추가한다. 새 UUID 라이브러리 없이 기존 pgx UUID 파서를 사용하며 positional argument는 거부한다.
- [ ] `RuntimeOperations.GenerateReport`, `BuildReportJob`과 수동 생성 factory를 추가하고 `BuildDigestJob`은 보존하되 `serve`에서 호출하지 않는다.
- [ ] `UIFactory`와 `ReportJobFactory`에 같은 `*report.FileStore`를 주입한다. `serve`가 한 번 열고 서버 종료 시 한 번 닫으며, CLI는 명령마다 열고 `defer Close`한다.
- [ ] `serve`는 collection+report scheduler, report file store, UI만 연결한다. SMTP/TestMail 콜백은 만들지 않는다.
- [ ] CLI 수동 생성은 runtime DB role로 실행하고 생성 여부, 공고 수, 상대 경로만 출력한다. 전체 `REPORT_DIR`은 출력하지 않는다.
- [ ] `Runtime`에 테스트 가능한 현재 사용자 조회 함수를 주입하고, 표준 `os/user.Current()`의 UID가 `0`이면 `generate-report`를 파일 생성 전에 거부한다. FreeBSD에서는 `daemon -f -u namo`로 실행해야 한다.
- [ ] 설정 경로 거부, SMTP 미설정 serve, 숨겨진 mail 명령, CLI tenant 검증, report job만 연결됨을 테스트한다.

**Verify:**

```powershell
go test -count=1 ./internal/config ./internal/cli ./internal/app -run 'Test.*(Config|Command|Runtime|GenerateReport|SendTestMail)'
```

## Task 8: 메일 없는 초대 링크 반환

**Files:**

- Modify: `internal/app/onboarding.go`
- Modify: `internal/app/onboarding_test.go`
- Modify: `internal/web/handler.go`
- Modify: `internal/web/onboarding_test.go`
- Modify: `web/templates/pages.html`
- Modify: `web/static/app.js`

- [ ] 웹 계약을 다음처럼 변경한다.

```go
type InvitationResult struct {
    URL       string
    ExpiresAt time.Time
}

type Onboarding interface {
    InviteTenant(context.Context, RequestContext, TenantInviteCommand) (InvitationResult, error)
    InviteMember(context.Context, RequestContext, MemberInviteCommand) (InvitationResult, error)
    Invitation(context.Context, string) (InvitationView, error)
    AcceptInvitation(context.Context, AcceptInviteCommand) error
}
```

- [ ] 기존 토큰 생성·SHA-256 저장·48시간 만료·단일 사용 로직은 유지한다. SMTP 메시지 생성과 재시도 함수는 보존하되 초대 실행 경로에서는 호출하지 않는다.
- [ ] 성공 POST에서 링크를 한 번만 렌더링하고 readonly input과 `링크 복사` 버튼을 제공한다. DB에는 hash만 전달되는지 테스트한다.
- [ ] 성공 응답에 `Cache-Control: no-store`, `Referrer-Policy: no-referrer`를 설정하고 링크를 query string, 로그, flash cookie에 넣지 않는다.
- [ ] Clipboard API 실패 시 input 선택으로 수동 복사가 가능하게 하며 JavaScript 없이도 링크 텍스트를 읽을 수 있게 한다.
- [ ] 권한, CSRF, mailer 호출 0회, 원문 토큰 DB 미전달, 헤더와 한 번 표시를 테스트한다.

**Verify:**

```powershell
go test -count=1 ./internal/app -run TestInvitation
go test -count=1 ./internal/web -run Test.*Invite
```

## Task 9: 리포트 웹 화면과 보안 다운로드 구현

**Files:**

- Modify: `internal/web/handler.go`
- Modify: `internal/web/handler_test.go`
- Modify: `internal/app/postgres_web.go`
- Modify: `internal/app/postgres_web_test.go`
- Modify: `web/templates/base.html`
- Modify: `web/templates/pages.html`
- Modify: `web/static/app.css`
- Modify: `web/static/app.js`

- [ ] `AppData`에 최근 리포트와 예약 정보를 추가한다.

```go
type ReportView struct {
    ID, FileName, Trigger, Status, DueAt, GeneratedAt string
    NoticeCount                                       int
    Downloadable                                      bool
}

type ReportDownload struct {
    Name     string
    Modified time.Time
    Body     io.ReadSeekCloser
}
```

- [ ] `Actions`에 `SaveReportSchedule`, `GenerateReport`, `RetryReport`, `OpenReport`를 추가한다. 기존 recipient/TestMail 메서드는 향후 메일 재개를 위해 구현에 남기되 UI 라우트에서 호출하지 않는다.
- [ ] 라우트를 구현한다: `GET /reports`, `POST /reports`, `POST /reports/generate`, `POST /reports/{uuid}/retry`, `GET|HEAD /reports/{uuid}/download`.
- [ ] `GET /notifications`는 `/reports`로 리다이렉트하고 `/notifications/recipients`, `/admin/test-mail` POST 실행 경로는 제거한다.
- [ ] 다운로드는 RLS가 적용된 DB에서 report 행을 먼저 조회한 뒤 저장된 상대 경로만 `FileStore.Open`에 전달한다. 타 테넌트 ID와 없는 ID는 모두 404를 반환한다.
- [ ] 다운로드 응답에 안전한 서버 생성 파일명, `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Content-Type: text/html; charset=utf-8`를 적용한다.
- [ ] 메뉴와 대시보드 문구를 리포트 저장/다음 리포트로 변경하고, 일정·최근 생성·파일명·공고 수·다운로드·`지금 생성`을 표시한다.
- [ ] 메일 발송은 `준비 중` disabled 항목으로 남긴다. 플랫폼 관리자 화면에서만 전체 `REPORT_DIR`을 표시한다.
- [ ] 수동 생성·일정 저장·실패 재시도는 tenant admin+CSRF를 요구하고, 일반 사용자는 목록/다운로드만 허용한다. 재시도 버튼은 3회 실패 상태에만 표시한다.
- [ ] 981×714 기준 이미지 색·선·카드 비율을 유지하고 1440×900, 1024×768, 390×844에서 반응형·키보드·대비를 점검한다.

**Verify:**

```powershell
go test -count=1 ./internal/web ./internal/app -run 'Test.*(Report|Download|Notification|Dashboard)'
```

## Task 10: FreeBSD 서비스·백업·운영 문서 전환

**Files:**

- Modify: `internal/config/freebsd_contract_test.go`
- Modify: `deploy/freebsd/namo.in`
- Modify: `deploy/freebsd/backup-namo.sh`
- Modify: `docs/operations-freebsd.md`
- Modify: `docs/zero-cost-stack.md`
- Modify: `docs/implementation/progress.md`
- Modify: `PRODUCT.md`

- [ ] 계약 테스트에서 env 로드 후 `REPORT_DIR` 검증, `install -d -o namo -g namo -m 0750`, `umask 027`, Nginx 미공개를 먼저 요구한다.
- [ ] `namo_prestart`는 빈 경로, `/`, 상대 경로, `..`, 최종 심볼릭 링크를 거부한 뒤 report dir과 기존 PID/log를 준비한다.
- [ ] Nginx에 `REPORT_DIR` alias나 정적 location을 추가하지 않는다.
- [ ] 백업 시작 시 namo가 실행 중이면 서비스를 중지하고 종료 trap으로 성공·실패와 관계없이 다시 시작한다. 중지된 상태에서 PostgreSQL dump 다음 report 디렉터리를 tar로 보관하고, DB dump·report archive·manifest를 하나의 백업 세트로 기록한다. 부분 실패 시 완성 manifest를 남기지 않는다.
- [ ] 운영 문서에 백업 중 짧은 서비스 중단이 발생함을 명시하고, 원래 중지 상태였다면 백업 후 임의로 시작하지 않게 테스트한다.
- [ ] 복구 문서에 DB 복원 후 report archive를 `/var/db/namo/reports`에 풀고 `namo:namo`, dir 0750/file 0640을 검증하는 순서를 추가한다.
- [ ] SMTP와 테스트 메일은 향후 기능으로 이동하고 env, 수동 생성, 예약 생성, 재시작 후 중복 없음, 다운로드, 백업 복원 절차를 문서화한다.
- [ ] root가 직접 `generate-report`를 실행해 root 소유 파일을 만들지 않도록 웹 버튼 또는 `daemon -f -u namo` 실행 예시를 사용한다.

**Verify:**

```powershell
go test -count=1 ./internal/config
```

FreeBSD에서:

```sh
sh -n deploy/freebsd/namo.in deploy/freebsd/backup-namo.sh
```

## Task 11: 전체 회귀와 nara Jail 인수 검증

**Files:**

- Update evidence: `docs/implementation/progress.md`
- Create evidence: `docs/implementation/reports/report-delivery-acceptance.md`

- [ ] 로컬 전체 검증을 실행하고 명령·결과·건너뛴 외부 검증을 기록한다.

```powershell
go test -count=1 ./...
go vet ./...
$env:CGO_ENABLED='0'; $env:GOOS='freebsd'; $env:GOARCH='amd64'
go build -trimpath -o build/namo-freebsd-amd64 .
```

- [ ] FreeBSD 14.3 nara Jail에서 네이티브 테스트와 race 검증을 실행한다.

```sh
go test -count=1 ./...
CGO_ENABLED=1 go test -race ./...
go vet ./...
go install
```

- [ ] 서비스 중지 → 바이너리/rc.d 설치 → migration → 시작 → `/healthz` 확인 순서로 적용한다. 실행 전 현재 env와 DB 백업을 만든다.
- [ ] `/var/db/namo/reports`와 생성 파일의 소유자·권한을 확인한다.
- [ ] 서로 다른 두 테넌트로 예약 리포트 각 1개, 빈 구간 0개, 수동 생성, 타 테넌트 다운로드 404를 검증한다.
- [ ] 서비스를 재시작하고 동일 예약 파일 수와 SHA-256이 변하지 않는지 확인한다.
- [ ] writer 완료 직후 DB finalize 실패를 재현하고 stale reclaim이 동일 파일로 복구하는지 확인한다.
- [ ] 3회 실패 행에서 관리자 재시도를 실행하고 새 report 행 없이 같은 ID·경로로 성공하는지 확인한다.
- [ ] 백업을 별도 임시 DB/디렉터리에 복구하여 DB report 행과 HTML 파일 SHA가 일치하는지 확인한다.
- [ ] 브라우저에서 4개 viewport와 초대 링크 복사를 검증한다.
- [ ] API 키·DB 비밀번호·세션 키·초대 토큰·절대 내부 경로를 인수 보고서에 기록하지 않는다.

## 완료 기준

- 고객사·예약 구간당 HTML 파일이 정확히 하나 생성된다.
- 신규 매칭이 없으면 파일과 report 행이 생성되지 않는다.
- 최대 3회 재시도, fencing token, stale claim 복구, 동일 SHA 복구가 검증된다.
- 다른 고객사의 목록과 파일은 직접 URL로도 접근할 수 없다.
- SMTP 설정 없이 `serve`가 시작되고 메일 실행 기능은 선택 불가 상태다.
- 초대 원문 토큰은 성공 화면에 한 번 표시되며 DB에는 hash만 남는다.
- FreeBSD rc.d 재시작, 백업·복구, 전체 테스트, vet, race, 교차 빌드가 검증된다.

## 실행 순서

1. Task 1~4를 파일 소유권이 겹치지 않게 병렬 진행한다.
2. Task 2~4 검토가 끝나면 Task 5~7을 순서대로 통합한다.
3. Task 8~9를 UI 에이전트가 진행하고 별도 보안 검토를 받는다.
4. Task 10을 적용한 뒤 Task 11을 루트 에이전트가 수행한다.
5. 각 Task 완료 후 사용자에게 변경 파일·테스트 결과를 짧게 보고하고 다음 단계 승인을 받는다.
