# PostgreSQL 릴리스 통합 테스트

`internal/app/postgres_release_integration_test.go`는 실제 PostgreSQL에서 다음 계약을 검증하는 opt-in 테스트다.

- 전체 임베디드 마이그레이션 적용, 재실행, 체크섬 불일치 차단
- 0004 마이그레이션의 대소문자 중복 수신자와 발송·스냅숏 참조 병합, 생존 수신자 키로 재시도 재개
- `FORCE ROW LEVEL SECURITY`에 의한 고객사 간 읽기·쓰기 차단
- 런타임 역할의 `invitations` 직접 변경 차단
- 두 런타임 연결 풀에서 공유하는 일일 API 호출량 상한
- 공고의 지역 조회 대기·완료 상태와 활성 공고 조회
- 만료된 메일 발송 claim의 이전 토큰 차단
- 초대 동시 수락 및 수락·재초대 경합의 단일 유효 결과

## 실행

전용 PostgreSQL 인스턴스의 마이그레이션 소유자와 런타임 로그인 역할 URL을 지정한다.

```powershell
$env:TEST_POSTGRES_OWNER_URL='postgres://owner:password@127.0.0.1:5432/postgres?sslmode=disable'
$env:TEST_POSTGRES_RUNTIME_URL='postgres://runtime_login:password@127.0.0.1:5432/postgres?sslmode=disable'
go test ./internal/app -run '^TestPostgresReleaseContracts$' -count=1 -v
```

두 URL 중 하나라도 없으면 테스트는 `skip`된다. 테스트는 기본적으로 `namo_test_<무작위값>` 데이터베이스를 만들고 종료할 때 삭제한다. 소유자에게 `CREATEDB`가 없으면 기존 데이터베이스를 자동으로 사용하지 않는다.

기존 데이터베이스를 재사용해야 할 때는 데이터베이스 이름이 `namo_test_`로 시작해야 하며, `TEST_POSTGRES_ALLOW_IN_PLACE`에 그 이름을 정확히 입력해야 한다. 이 모드는 실행 전후 `public` 스키마를 삭제하므로 반드시 폐기 가능한 테스트 데이터베이스에서만 사용한다.

마이그레이션이 생성하는 전역 PostgreSQL 역할은 데이터베이스 삭제로 제거되지 않는다. 따라서 운영 서버가 아닌 격리된 테스트 인스턴스를 사용해야 한다. 이 테스트는 나라장터 API, SMTP, Nginx, FreeBSD 서비스 동작을 검증하지 않는다.
