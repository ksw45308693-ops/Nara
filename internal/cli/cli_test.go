package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"namo/internal/config"
)

func TestRunPrintsUsageWithoutCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false

	code := Run(context.Background(), nil, nil, func(context.Context, string, config.Config, []string) error {
		called = true
		return nil
	}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if called {
		t.Fatal("executor called without a command")
	}
	if !strings.Contains(stderr.String(), "사용법: namo ") || !strings.Contains(stderr.String(), "serve") || !strings.Contains(stderr.String(), "collect-once") {
		t.Fatalf("usage missing commands: %q", stderr.String())
	}
}

func TestRunExecutesValidatedCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotCommand string
	lookup := lookupMap(map[string]string{
		"DATABASE_URL":           "postgres://runtime@localhost/monitor",
		"MIGRATION_DATABASE_URL": "postgres://owner@localhost/monitor",
		"SESSION_KEY":            strings.Repeat("x", 32),
	})

	code := Run(context.Background(), []string{"migrate"}, lookup, func(_ context.Context, command string, _ config.Config, _ []string) error {
		gotCommand = command
		return nil
	}, &stdout, &stderr)

	if code != 0 || gotCommand != "migrate" {
		t.Fatalf("Run() code = %d command = %q stderr = %q", code, gotCommand, stderr.String())
	}
}

func TestRunRejectsMissingCommandConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookup := lookupMap(map[string]string{
		"DATABASE_URL":           "postgres://runtime@localhost/monitor",
		"MIGRATION_DATABASE_URL": "postgres://owner@localhost/monitor",
		"SESSION_KEY":            strings.Repeat("x", 32),
	})

	code := Run(context.Background(), []string{"collect-once"}, lookup, func(context.Context, string, config.Config, []string) error {
		t.Fatal("executor called with invalid command configuration")
		return nil
	}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stderr.String(), "G2B_API_KEY") {
		t.Fatalf("Run() code = %d stderr = %q", code, stderr.String())
	}
}

func TestRunReportsExecutorFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookup := lookupMap(map[string]string{
		"DATABASE_URL":           "postgres://runtime@localhost/monitor",
		"MIGRATION_DATABASE_URL": "postgres://owner@localhost/monitor",
		"SESSION_KEY":            strings.Repeat("x", 32),
	})

	code := Run(context.Background(), []string{"migrate"}, lookup, func(context.Context, string, config.Config, []string) error {
		return errors.New("database unavailable")
	}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stderr.String(), "database unavailable") {
		t.Fatalf("Run() code = %d stderr = %q", code, stderr.String())
	}
}

func lookupMap(values map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
