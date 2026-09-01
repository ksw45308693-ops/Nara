# 나라장터 입찰공고 모니터링

나라장터 공고를 정기 수집하고 고객사 규칙으로 선별해 웹 화면과 일일 메일로 제공하는 Go 서비스다.

## 구성

- Go 1.27 단일 서비스
- PostgreSQL
- 서버 렌더링 HTML, CSS, 최소 JavaScript
- FreeBSD amd64, Nginx, `rc.d`

## 명령

```text
g2b-monitor migrate
g2b-monitor create-admin --email admin@example.internal --name 관리자 --password-file /secure/admin.pass
g2b-monitor collect-once
g2b-monitor send-test-mail --to admin@example.internal
g2b-monitor serve
```

환경 변수 예시는 `.env.example`에 있다. 실제 비밀값 파일은 저장소에 포함하지 않는다.
`serve`의 `BASE_URL`은 경로·쿼리 없는 HTTPS origin, `LISTEN_ADDR`은 1~65535의
loopback 포트만 허용한다.
`DATABASE_URL`은 `NOBYPASSRLS` 서비스 계정, `MIGRATION_DATABASE_URL`은 PostgreSQL
superuser 계정으로 반드시 분리한다. `CREATEROLE`만 있는 계정은 사용할 수 없다. 마이그레이션이
`BYPASSRLS` 보조 역할을 고정하고 그 역할 소유 함수를 안전하게 교체하기 때문이다. 관리자 URL은
`migrate`와 `create-admin`을 마친 뒤 서비스 환경에서 제거한다.

## 로컬 확인

```text
go test ./...
go test -race ./...
go vet ./...
```

FreeBSD 교차 빌드:

```text
$env:CGO_ENABLED='0'
$env:GOOS='freebsd'
$env:GOARCH='amd64'
go build -o build/g2b-monitor-freebsd-amd64 ./cmd/g2b-monitor
```

실서버 절차는 `docs/operations-freebsd.md`를 따른다.
유료 서비스 없이 운영하는 범위와 전제는 `docs/zero-cost-stack.md`에 정리했다.
