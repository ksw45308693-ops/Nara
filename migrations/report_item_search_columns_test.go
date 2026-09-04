package migrations

import (
	"strings"
	"testing"
)

func TestReportItemSearchColumnsMigrationExists(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, migration := range all {
		if migration.Version == 16 {
			body = migration.SQL
		}
	}
	if body == "" {
		t.Fatal("migration 16 is missing")
	}
	for _, want := range []string{"source_kind", "posted_at", "collected_at", "recorded_at", "namo_runtime"} {
		if !strings.Contains(body, want) {
			t.Fatalf("migration 16 missing %q", want)
		}
	}
}
