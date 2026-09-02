package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"namo/internal/auth"
	"namo/internal/config"
)

func TestRuntimeExecuteDispatchesOneKnownCommand(t *testing.T) {
	var called string
	runtime := &Runtime{Operations: RuntimeOperations{
		CollectOnce: func(_ context.Context, _ config.Config, args []string) error {
			called = strings.Join(args, ",")
			return nil
		},
	}}

	err := runtime.Execute(context.Background(), "collect-once", config.Config{}, []string{"unexpected"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if called != "unexpected" {
		t.Fatalf("handler args = %q", called)
	}
}

func TestRuntimeExecuteRejectsUnavailableHandler(t *testing.T) {
	err := (&Runtime{}).Execute(context.Background(), "serve", config.Config{}, nil)
	if err == nil || !strings.Contains(err.Error(), "serve") {
		t.Fatalf("Execute() error = %v, want unavailable serve handler", err)
	}
}

func TestParseCreateAdminReadsPasswordOnlyFromExplicitStdin(t *testing.T) {
	options, password, err := parseCreateAdminOptions(
		[]string{"--email", " Admin@Example.COM ", "--name", " 운영자 ", "--password-stdin"},
		strings.NewReader("correct horse battery staple\r\n"),
		func(string) (io.ReadCloser, error) { return nil, errors.New("must not open a file") },
	)
	if err != nil {
		t.Fatalf("parseCreateAdminOptions() error = %v", err)
	}
	if options.Email != "admin@example.com" || options.DisplayName != "운영자" {
		t.Fatalf("options = %#v", options)
	}
	if password != "correct horse battery staple" {
		t.Fatalf("password = %q", password)
	}
}

func TestParseCreateAdminRequiresExactlyOneSecretSource(t *testing.T) {
	for _, args := range [][]string{
		{"--email", "admin@example.com"},
		{"--email", "admin@example.com", "--password-stdin", "--password-file", "secret.txt"},
		{"--email", "admin@example.com", "--password", "visible"},
	} {
		_, _, err := parseCreateAdminOptions(args, strings.NewReader("long-enough-password"), nil)
		if err == nil {
			t.Fatalf("parseCreateAdminOptions(%q) unexpectedly succeeded", args)
		}
	}
}

func TestParseCreateAdminRejectsWeakOrOversizedPasswords(t *testing.T) {
	for _, password := range []string{"too-short", strings.Repeat("x", 73)} {
		_, _, err := parseCreateAdminOptions(
			[]string{"--email", "admin@example.com", "--password-stdin"},
			strings.NewReader(password), nil,
		)
		if err == nil || !strings.Contains(err.Error(), "password") {
			t.Fatalf("password length %d error = %v", len(password), err)
		}
	}
}

type capturingAdminExecer struct {
	args []any
	err  error
}

func (e *capturingAdminExecer) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	e.args = append([]any(nil), args...)
	return pgconn.CommandTag{}, e.err
}

func TestInsertPlatformAdminStoresBcryptHash(t *testing.T) {
	store := &capturingAdminExecer{}
	err := insertPlatformAdmin(context.Background(), store, CreateAdminOptions{
		Email: "admin@example.com", DisplayName: "운영자",
	}, "correct horse battery staple")
	if err != nil {
		t.Fatalf("insertPlatformAdmin() error = %v", err)
	}
	if len(store.args) != 3 {
		t.Fatalf("Exec args = %#v", store.args)
	}
	hash, ok := store.args[2].(string)
	if !ok || hash == "correct horse battery staple" || !auth.CheckPassword(hash, "correct horse battery staple") {
		t.Fatalf("stored password is not a valid bcrypt hash: %#v", store.args[2])
	}
}

type scriptedMailer struct {
	failures int
	calls    int
	from     string
	to       string
	message  []byte
}

func (m *scriptedMailer) Send(_ context.Context, from, to string, message []byte) error {
	m.calls++
	m.from, m.to, m.message = from, to, append([]byte(nil), message...)
	if m.calls <= m.failures {
		return errors.New("temporary SMTP failure")
	}
	return nil
}

func TestSendTestMailRetriesAndBuildsSafeMessage(t *testing.T) {
	mailer := &scriptedMailer{failures: 2}
	err := sendTestMail(context.Background(), mailer, "monitor@example.com", "recipient@example.com")
	if err != nil {
		t.Fatalf("sendTestMail() error = %v", err)
	}
	if mailer.calls != 3 {
		t.Fatalf("Send() calls = %d, want 3", mailer.calls)
	}
	if mailer.from != "monitor@example.com" || mailer.to != "recipient@example.com" {
		t.Fatalf("from/to = %q/%q", mailer.from, mailer.to)
	}
	if !bytes.Contains(mailer.message, []byte("MIME-Version: 1.0")) {
		t.Fatalf("message lacks MIME headers: %q", mailer.message)
	}
}

func TestParseSendTestMailRequiresPlainMailbox(t *testing.T) {
	to, err := parseTestMailOptions([]string{"--to", "recipient@example.com"})
	if err != nil || to != "recipient@example.com" {
		t.Fatalf("parseTestMailOptions() = %q, %v", to, err)
	}
	for _, args := range [][]string{
		{},
		{"--to", "Recipient <recipient@example.com>"},
		{"--to", "recipient@example.com", "extra"},
	} {
		if _, err := parseTestMailOptions(args); err == nil {
			t.Fatalf("parseTestMailOptions(%q) unexpectedly succeeded", args)
		}
	}
}

func TestRejectCommandArguments(t *testing.T) {
	if err := rejectCommandArguments("migrate", nil); err != nil {
		t.Fatalf("rejectCommandArguments(nil) error = %v", err)
	}
	if err := rejectCommandArguments("migrate", []string{"extra"}); err == nil {
		t.Fatal("rejectCommandArguments(extra) unexpectedly succeeded")
	}
}

type cancelingScheduler struct {
	cancel context.CancelFunc
	calls  int
}

func (s *cancelingScheduler) Tick(context.Context, time.Time) error {
	s.calls++
	s.cancel()
	return errors.New("expected test failure")
}

func TestRunSchedulerTicksImmediatelyAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &cancelingScheduler{cancel: cancel}
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, scheduler, io.Discard)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runScheduler did not stop after cancellation")
	}
	if scheduler.calls != 1 {
		t.Fatalf("Tick() calls = %d, want immediate call", scheduler.calls)
	}
}
