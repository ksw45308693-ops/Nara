# namo

나라장터 공고를 정기 수집하고 고객사 규칙으로 선별해 웹 화면과 HTML 리포트로 제공하는 Go 서비스다.

## 구성

- Go 1.27 단일 서비스
- PostgreSQL
- 서버 렌더링 HTML, CSS, 최소 JavaScript
- FreeBSD amd64, Nginx, `rc.d`

## 설치

```text
go test ./...
go install
```

FreeBSD `root` 계정에서 `GOBIN`을 따로 설정하지 않았다면 실행 파일은
`/root/go/bin/namo`에 설치된다.

## 명령

```text
namo migrate
namo create-admin --email admin@example.internal --name 관리자 --password-file /secure/admin.pass
namo collect-once
namo generate-report --tenant 11111111-1111-1111-1111-111111111111
namo serve
```

환경 변수 예시는 `.env.example`에 있다. 실제 비밀값 파일은 저장소에 포함하지 않는다.
`serve`의 `BASE_URL`은 경로·쿼리 없는 HTTPS origin, `LISTEN_ADDR`은 1~65535의
loopback 포트만 허용한다. `serve`와 `generate-report`는 `DELIVERY_MODE=report`와
기존의 안전한 절대 디렉터리인 `REPORT_DIR`이 필요하다. 파일시스템 루트와 심볼릭 링크는
리포트 디렉터리로 사용할 수 없다.
`DATABASE_URL`은 `NOBYPASSRLS` 서비스 계정, `MIGRATION_DATABASE_URL`은 PostgreSQL
superuser 계정으로 반드시 분리한다. `CREATEROLE`만 있는 계정은 사용할 수 없다. 마이그레이션이
`BYPASSRLS` 보조 역할을 고정하고 그 역할 소유 함수를 안전하게 교체하기 때문이다. 관리자 URL은
`migrate`와 `create-admin`을 마친 뒤 서비스 환경에서 제거한다.

FreeBSD에서 수동 리포트 생성은 `root`로 실행하지 않는다. 서비스 계정으로 실행한다.

```text
daemon -f -u namo namo generate-report --tenant 11111111-1111-1111-1111-111111111111
```

## 계정

계정은 두 경로로 만든다.

- 회원가입: `/signup`에서 이메일과 비밀번호만 입력한다. 가입하면 바로 로그인되지만
  회사(테넌트)가 배정되지 않은 상태이므로 대기 화면만 보인다.
- 초대: 플랫폼 관리자가 테넌트를 만들고 `/accept-invite` 링크로 관리자 계정을 만든다.

가입 계정은 `member` 권한으로 생성되고 회사 데이터에 접근하지 않는다. 플랫폼 관리자가
`/admin`의 `회원 계정 배정`에서 회사를 배정하거나 해제한다. 배정 결과는 해당 사용자의
다음 요청부터 적용된다. 비밀번호는 UTF-8 기준 12~72바이트로 제한한다.

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
go build -trimpath -o build/namo-freebsd-amd64 .
```

실서버 절차는 `docs/operations-freebsd.md`를 따른다.
유료 서비스 없이 운영하는 범위와 전제는 `docs/zero-cost-stack.md`에 정리했다.
