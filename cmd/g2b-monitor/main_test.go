package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"g2b-monitor/internal/config"
)

func TestRunWiresCLIExecutor(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookup := func(key string) (string, bool) {
		values := map[string]string{
			"DATABASE_URL":           "postgres://runtime@localhost/monitor",
			"MIGRATION_DATABASE_URL": "postgres://owner@localhost/monitor",
			"SESSION_KEY":            strings.Repeat("s", 32),
		}
		value, ok := values[key]
		return value, ok
	}
	called := false
	code := run(context.Background(), []string{"migrate"}, lookup,
		func(context.Context, string, config.Config, []string) error {
			called = true
			return nil
		}, &stdout, &stderr)
	if code != 0 || !called {
		t.Fatalf("run() code = %d called = %v stderr = %q", code, called, stderr.String())
	}
}
