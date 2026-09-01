# FreeBSD 내부 검증 운영 절차

## 준비

1. PostgreSQL, Nginx, Go 빌드 결과를 준비한다.
2. OS 전용 사용자 `g2b_monitor`와 데이터베이스를 만든다.
3. 실행 파일을 `/usr/local/sbin/g2b-monitor`에 설치한다.
4. 서비스 환경은 `/usr/local/etc/g2b-monitor.env`, 초기화 전용 환경은 `/usr/local/etc/g2b-monitor-owner.env`로 분리한다. 두 파일은 `root:wheel`, `0600`으로 둔다. 서비스 파일에는 `MIGRATION_DATABASE_URL`을 넣지 않는다.
5. `deploy/freebsd/g2b_monitor.in`을 `/usr/local/etc/rc.d/g2b_monitor`에 설치하고 실행 권한을 준다.
6. `deploy/freebsd/newsyslog.conf`를 `/etc/newsyslog.conf.d/g2b_monitor.conf`에 설치한다. `daemon -H`와 supervisor PID 파일을 사용하므로 회전 뒤 HUP을 받은 `daemon`이 로그 파일을 다시 연다.

`serve`는 `BASE_URL`의 HTTPS scheme과 `LISTEN_ADDR`의 loopback 주소를 강제한다.
TLS는 Nginx가 종료하고 Go 서비스 포트는 외부 인터페이스에 직접 노출하지 않는다.

## 초기화와 실행

PostgreSQL 관리자 셸에서 DB를 먼저 만든다. 현재 마이그레이션은 `BYPASSRLS` 보조 역할을
생성·고정하고 해당 `NOLOGIN` 역할 소유 함수를 교체한다. 따라서 `MIGRATION_DATABASE_URL`에는
PostgreSQL superuser 자격증명이 필요하며 `CREATEROLE`만 있는 계정은 사용할 수 없다. 명령은
연결 직후 `rolsuper`를 검사하고 조건을 만족하지 않으면 SQL 실행 전에 중단한다.
이 자격증명은 서비스가 읽지 않으며 초기화가 끝나면 파일을 제거한다.

```sql
CREATE DATABASE g2b_monitor;
```

초기화 전용 환경을 현재 셸에 읽힌 뒤 마이그레이션을 실행한다. 이 환경은
`MIGRATION_DATABASE_URL`에는 PostgreSQL 관리자 URL, `DATABASE_URL`에는 별도의 서비스 URL을 둔다.

```sh
set -a
. /usr/local/etc/g2b-monitor-owner.env
set +a
/usr/local/sbin/g2b-monitor migrate
install -m 0600 /dev/null /root/g2b-admin.pass
# /root/g2b-admin.pass에 12~72바이트의 초기 비밀번호 한 줄을 입력한다.
/usr/local/sbin/g2b-monitor create-admin --email admin@example.internal --name 관리자 --password-file /root/g2b-admin.pass
rm -f /root/g2b-admin.pass
unset MIGRATION_DATABASE_URL
rm -f /usr/local/etc/g2b-monitor-owner.env
```

마이그레이션이 만든 `g2b_runtime` 권한 그룹에 로그인 역할을 연결한다.

```sql
CREATE ROLE g2b_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'change-runtime-password';
GRANT g2b_runtime TO g2b_app;
```

서비스 환경의 `DATABASE_URL`은 `g2b_app`만 사용한다. 역할 속성을 확인한 뒤 시작한다.

```sql
SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
FROM pg_roles WHERE rolname IN ('g2b_app', 'g2b_runtime', 'g2b_auth_definer');
```

```sh
sysrc g2b_monitor_enable=YES
service g2b_monitor start
service g2b_monitor status
newsyslog -nvv -f /etc/newsyslog.conf.d/g2b_monitor.conf
```

`daemon -r -R 5`는 Go child가 비정상 종료되면 5초 뒤 다시 시작한다. 정상적인
`service g2b_monitor stop`은 supervisor를 종료하므로 재시작하지 않는다. 다음 검사는
운영과 분리된 검증용 Jail에서만 실행한다.

```sh
g2b_supervisor_pid="$(cat /var/run/g2b_monitor.pid)"
g2b_child_pid="$(pgrep -P "$g2b_supervisor_pid" | head -n 1)"
test -n "$g2b_child_pid"
kill -KILL "$g2b_child_pid"
sleep 6
g2b_new_child_pid="$(pgrep -P "$g2b_supervisor_pid" | head -n 1)"
test -n "$g2b_new_child_pid" && test "$g2b_new_child_pid" != "$g2b_child_pid"
service g2b_monitor stop
sleep 1
! kill -0 "$g2b_supervisor_pid" 2>/dev/null
service g2b_monitor start
```

서비스 환경을 관리자 셸에만 읽어 실제 수집과 테스트 메일을 점검할 수 있다.

```sh
set -a
. /usr/local/etc/g2b-monitor.env
set +a
/usr/local/sbin/g2b-monitor collect-once
/usr/local/sbin/g2b-monitor send-test-mail --to admin@example.internal
```

Nginx 설정을 설치한 뒤 적용 전에 검사한다. `deploy/nginx/g2b-monitor-http.conf`는
반드시 Nginx `http {}` 안에서 서버 설정보다 먼저 읽히게 하고,
`deploy/nginx/g2b-monitor.conf`는 같은 `http {}` 안에 설치한다. 전자는
`POST /login`만 클라이언트 주소별로 제한한다. 초과 요청은 HTTP 429를 반환하며
로그인 화면을 여는 GET 요청은 제한 키가 비어 있으므로 계속 허용된다.

```sh
nginx -t
service nginx reload
sockstat -4 -6 -l | grep -E ':(443|8080)'
fetch -qo- https://g2b-monitor.example.internal/healthz
```

## 백업과 복구

백업은 PostgreSQL 서버에서 실행한다. 제공 스크립트는 `umask 077`로
`pg_dumpall --globals-only`와 custom-format `pg_dump`를 실행한다. 고정된 전용
디렉터리 안에서 `mktemp`로 `0600` 임시 파일을 만들고, dump가 비어 있지 않은지와
`pg_restore --list` 결과를 확인한 뒤 `mv -f`로 같은 파일시스템에서 원자적으로
이름을 바꾼다. 각 결과에는 UTC 시각과 PID가 붙으므로 이전 정상 세대를 덮어쓰거나
자동 삭제하지 않는다. 두 파일을 모두 게시한 뒤에만 같은 세대의
`complete-<세대>.manifest`를 원자적으로 게시한다. 완료 마커가 없는 세대는 중단된
백업으로 간주하며 복구에 사용하지 않는다. 스크립트는 `su -l`과 `env -i`를 사용하고 `PGHOST=/tmp`,
`PGPORT=5432`, `PGUSER=postgres`를 명시하므로 실행자에게 남은 `PGHOST`, `PGPORT`,
`PGSERVICE`, `PGUSER` 등으로 다른 PostgreSQL 클러스터를 선택하지 않는다. 실제
운영 클러스터의 로컬 socket이나 port가 다르면 적용 전에 스크립트의 고정값을 수정한다.

```sh
install -o root -g wheel -m 0700 deploy/freebsd/backup-g2b-monitor.sh /usr/local/sbin/g2b-monitor-backup
/usr/local/sbin/g2b-monitor-backup
stat -f '%Su:%Sg %Lp %N' /var/backups/g2b-monitor/complete-*.manifest /var/backups/g2b-monitor/globals-*.sql /var/backups/g2b-monitor/g2b_monitor-*.dump
```

`globals-<세대>.sql`에는 역할과 비밀번호 해시가 들어갈 수 있으므로 디렉터리는
`0700`, 파일은 `0600`을 유지한다. 복구할 한 세대의 globals 파일과 dump 파일을
확정하고 아래 세 변수에 같은 세대의 절대 경로를 지정한다. 완료 마커에 두 경로가
정확히 기록된 경우에만 복구하며, 역할을 먼저 복구한 뒤
데이터베이스를 복구한다. 기존 클러스터에서는 같은 역할이 이미 있어 충돌할 수 있으므로,
다음 시험은 운영과 분리된 일회용 Jail의 깨끗한 PostgreSQL 클러스터에서 수행한다.

```sh
install -d -o postgres -g postgres -m 0700 /var/tmp/g2b-monitor-restore-cluster
restore_globals='/var/backups/g2b-monitor/globals-YYYYMMDDTHHMMSSZ-PID.sql'
restore_database='/var/backups/g2b-monitor/g2b_monitor-YYYYMMDDTHHMMSSZ-PID.dump'
restore_complete='/var/backups/g2b-monitor/complete-YYYYMMDDTHHMMSSZ-PID.manifest'
test -s "$restore_complete" && test -s "$restore_globals" && test -s "$restore_database"
grep -Fqx "$restore_globals" "$restore_complete" && grep -Fqx "$restore_database" "$restore_complete"
su -l postgres -c "env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin pg_restore --list '$restore_database' >/dev/null"
su -l postgres -c 'env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin initdb -D /var/tmp/g2b-monitor-restore-cluster/data -U restore_admin --auth-local=trust --auth-host=scram-sha-256 --no-locale -E UTF8'
su -l postgres -c 'env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin pg_ctl -D /var/tmp/g2b-monitor-restore-cluster/data -o "-p 55432 -k /var/tmp/g2b-monitor-restore-cluster" -w start'
su -l postgres -c "env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin psql -U restore_admin -h /var/tmp/g2b-monitor-restore-cluster -p 55432 -v ON_ERROR_STOP=1 -d postgres -f '$restore_globals'"
su -l postgres -c 'env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin createdb -U restore_admin -h /var/tmp/g2b-monitor-restore-cluster -p 55432 g2b_monitor_restore_test'
su -l postgres -c "env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin pg_restore --exit-on-error -U restore_admin -h /var/tmp/g2b-monitor-restore-cluster -p 55432 -d g2b_monitor_restore_test '$restore_database'"
su -l postgres -c 'env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin psql -U restore_admin -h /var/tmp/g2b-monitor-restore-cluster -p 55432 -v ON_ERROR_STOP=1 -d g2b_monitor_restore_test -c "SELECT count(*) FROM public.schema_migrations"'
su -l postgres -c 'env -i PATH=/bin:/usr/bin:/usr/local/bin:/usr/local/sbin pg_ctl -D /var/tmp/g2b-monitor-restore-cluster/data -m fast -w stop'
```

검증을 마친 뒤 정확한 시험 경로 `/var/tmp/g2b-monitor-restore-cluster`만 제거한다. 운영 서버의 PostgreSQL 데이터 경로에는 이 절차를 실행하지 않는다.

## 메일 발송 보장 범위

발송 행은 고정된 발송 구간과 수신자별 delivery key로 잠근다. 같은 key와 같은
공고 본문은 항상 같은 `Message-ID`를 사용한다. 재시도 전에 일부 공고가 마감되어
본문이 달라지면 다른 ID를 사용하고, 모든 공고가 마감되면 미발송 delivery를 만료
취소 사유와 함께 종료해 더는 보내지 않는다. 다만 SMTP 서버가 수락한 직후 서비스가
종료되고 PostgreSQL 성공 기록 전에 멈추면 복구 후 같은 메일이 다시 전송될 수 있다.
SMTP와 PostgreSQL 사이에는 단일 원자적 커밋이 없으므로 이 경계는 exactly-once가
아니라 at-least-once다. 플랫폼 작업 상태에서 실패한 delivery를 확인하고, 메일 서버가
`Message-ID` 중복 억제를 지원하는지 실제 릴레이에서 검증한다.

## 검증 경계

- 교차 빌드 성공은 FreeBSD 런타임 성공을 증명하지 않는다.
- API fixture 테스트는 실제 서비스 키·호출량·현재 응답을 검증하지 않는다.
- SMTP fake 테스트는 사내 메일 릴레이·스팸 정책·수신을 검증하지 않는다.
- `daemon -H`/newsyslog 설정 검사는 실제 회전 뒤 새 로그에 계속 기록되는지를 검증하지 않는다.
- 실제 서버에서는 마이그레이션, 네 업무구분 수집, 필터, 메일, 재시작, 백업 복구를 각각 확인한다.
