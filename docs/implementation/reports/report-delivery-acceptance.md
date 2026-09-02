# 리포트 전달 로컬 인수 증거

검증일: 2026-09-02

## 판정

- Task 11의 로컬 검증 단계는 완료됐다.
- 전체 Task 11은 nara Jail 인수 검증 전이므로 완료되지 않았다.
- Jail 배포와 실서버 검증은 사용자의 명시적 승인 후 진행한다.
- 이번 단계에서는 Git 명령, 원격 접속, 서버 변경을 수행하지 않았다.

## Windows 검증

| 명령 또는 검사 | 결과 |
| --- | --- |
| `go test -count=1 ./...` | PASS. 모든 패키지가 통과했다. |
| `go vet ./...` | PASS |
| `go mod verify` | PASS. `all modules verified` |
| 변경된 Go 파일의 `gofmt` 검사 | PASS. 출력 없음 |
| `go list -f` | `name=main import=namo`; 설치 대상은 로컬 `<GOBIN>/namo.exe` |
| `go install .` | PASS |
| 설치 바이너리를 인자 없이 실행 | `사용법: namo <serve|migrate|create-admin|collect-once|generate-report>` 출력 후 exit 2 |
| 레거시 제품명 검사 | `build`와 `.git` 밖에서 이전 하이픈형·밑줄형 제품명 패턴 없음 |

## FreeBSD 교차 빌드

- 파일: `build/namo-freebsd-amd64`
- 크기: 21,920,119 bytes
- SHA-256: `11B12EC9A934310528DE6C7CBCB72D3A5B85AC1DC8C3C0A59DCD5EEE2CB37939`

교차 빌드는 FreeBSD용 컴파일만 증명한다. FreeBSD 실행은 증명하지 않는다.

## WSL 및 Linux 검증

| 명령 또는 검사 | 결과 |
| --- | --- |
| Go 1.27, GCC, `CGO_ENABLED=1 go test -race -count=1 ./...` | PASS. 모든 패키지가 통과했다. |
| `go vet ./...` | PASS |
| FreeBSD 스크립트 3개의 `sh -n` | PASS |
| `sh deploy/freebsd/freebsd_scripts_test.sh` | PASS. `freebsd scripts: PASS` |

스크립트 검증은 WSL의 구문 검사와 mock 기반 실행 계약이다. FreeBSD의 `rc.subr`, 서비스, 파일 소유권, PostgreSQL 도구 실행은 증명하지 않는다.

## 로컬 브라우저 표본 QA

- 981×714, 1440×900, 1024×768, 390×844에서 가로 overflow, 검사 대상 텍스트 잘림, console warning 또는 error가 없었다.
- 390×844에서 배경 `inert`, Tab과 Shift+Tab focus trap, Escape 닫기, toggle focus 복원, skip-link 포커스를 확인했다.
- 주요 표본의 텍스트 대비는 4.88:1~13.72:1이었다.
- Impeccable detector는 parser module이 없어 regex fallback을 사용했고 0건을 반환했다. 이 결과는 detector clean 판정이 아니다.

## 명시적으로 건너뛴 PostgreSQL 테스트

다음 테스트는 `TEST_POSTGRES_OWNER_URL`과 `TEST_POSTGRES_RUNTIME_URL`이 없어 verbose 실행에서 SKIP됐다.

- `TestPostgresReleaseContracts`
- `TestPostgresReportRepositoryClaimsFencesSnapshotsAndIsolatesTenants`
- `TestPostgresReportSchema`

명령 suite 자체는 PASS다. 이 세 테스트는 실제 PostgreSQL에서 검증되지 않았다.
DB 연결 없이 실행되는 `TestPostgresReportSchemaMigrationBaseline`은 PASS해 0007 기준선과 0008 이후 전체 업그레이드 위치를 확인했다.

`G2B_API_KEY`와 `DATABASE_URL`도 없었다. 실제 나라장터 API, PostgreSQL, SMTP 호출은 실행하지 않았다.

## 독립 검토

- Task 9 수정 후 검토: ADDRESSED. Critical 0, Important 0, Minor 0.
- Task 10 Fix round 3 최종 검토: ADDRESSED. Critical 0, Important 0, Minor 0.
- 최종 교차 검토 지적 사항인 빈 수동 생성 안내, 마이그레이션 개수 계약, 인수 문서 경로 비식별화를 수정했다.

## 남은 Jail 인수 범위

다음 항목은 아직 검증하지 않았다.

- FreeBSD 또는 nara Jail의 네이티브 테스트, race, vet, install
- 서비스 중지, 백업, 바이너리와 rc.d 설치, migration, 시작, `/healthz`
- Nginx 설정과 authenticated report download
- `REPORT_DIR`과 HTML 파일의 실제 소유자 및 권한
- 실제 DB의 RLS, 동시 claim, fencing, stale reclaim, 3회 제한, tenant 격리
- 예약·수동 리포트, 빈 구간 미생성, 재 restart 후 동일 파일과 SHA 유지
- 실제 backup과 별도 환경 restore, DB 행과 HTML SHA 비교
- 배포된 서비스의 live browser acceptance

Jail, 서비스, Nginx, 실제 DB, backup restore, live browser 상태는 변경하거나 검증하지 않았다.
