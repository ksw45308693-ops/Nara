package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFreeBSDLogRotationAndBackupContracts(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	read := func(path string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}

	rc := read("deploy/freebsd/namo.in")
	for _, want := range []string{`procname="/usr/sbin/daemon"`, `-r -R 5`, `-H -P ${pidfile}`, `-o ${namo_log_file}`} {
		if !strings.Contains(rc, want) {
			t.Errorf("rc.d script missing %q", want)
		}
	}
	envSource := strings.Index(rc, `. "$namo_env_file"`)
	unsetOwnerURL := strings.LastIndex(rc, "unset MIGRATION_DATABASE_URL")
	if envSource < 0 || unsetOwnerURL < envSource {
		t.Error("rc.d must unset MIGRATION_DATABASE_URL after loading the runtime environment")
	}
	rotation := read("deploy/freebsd/newsyslog.conf")
	for _, want := range []string{"/var/run/namo.pid", "HUP"} {
		if !strings.Contains(rotation, want) {
			t.Errorf("newsyslog configuration missing %q", want)
		}
	}
	backup := read("deploy/freebsd/backup-namo.sh")
	for _, want := range []string{"mktemp", "umask 077", "su -l postgres", "env -i", "PGHOST='$pg_host'", "PGPORT='$pg_port'", "PGUSER='$pg_user'", "pg_dumpall --globals-only", "pg_restore --list", "test -s", "mv -f", "complete-${backup_stamp}.manifest", "completion_tmp"} {
		if !strings.Contains(backup, want) {
			t.Errorf("backup script missing %q", want)
		}
	}
	if databasePublish, completionPublish := strings.Index(backup, `mv -f "$database_tmp" "$database_final"`), strings.Index(backup, `mv -f "$completion_tmp" "$completion_final"`); databasePublish < 0 || completionPublish < databasePublish {
		t.Error("backup completion marker must be published after both backup files")
	}
	if strings.Contains(backup, "su -m postgres") {
		t.Error("backup script must not preserve the caller PostgreSQL environment")
	}
	operations := read("docs/operations-freebsd.md")
	for _, want := range []string{"backup-namo.sh", "restore_globals", "restore_database", "restore_complete", "pg_restore", "Message-ID", "at-least-once", "mktemp", "pg_restore --list", "mv -f"} {
		if !strings.Contains(operations, want) {
			t.Errorf("operations guide missing %q", want)
		}
	}
}

func TestNginxLimitsLoginPostsWithoutThrottlingLoginPage(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	read := func(path string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}

	httpConfig := read("deploy/nginx/namo-http.conf")
	for _, want := range []string{"map $request_method $namo_login_limit_key", "POST $binary_remote_addr", `default ""`, "limit_req_zone $namo_login_limit_key"} {
		if !strings.Contains(httpConfig, want) {
			t.Errorf("Nginx HTTP login limit config missing %q", want)
		}
	}
	serverConfig := read("deploy/nginx/namo.conf")
	for _, want := range []string{"location = /login", "limit_req zone=namo_login", "limit_req_status 429"} {
		if !strings.Contains(serverConfig, want) {
			t.Errorf("Nginx server login limit config missing %q", want)
		}
	}
	operations := read("docs/operations-freebsd.md")
	for _, want := range []string{"namo-http.conf", "HTTP 429", "POST /login"} {
		if !strings.Contains(operations, want) {
			t.Errorf("operations guide missing login rate-limit contract %q", want)
		}
	}
}
