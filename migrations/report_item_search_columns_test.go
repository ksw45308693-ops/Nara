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
	for _, want := range []string{
		"ALTER TABLE public.report_items ADD COLUMN source_kind text;",
		"ALTER TABLE public.report_items ADD COLUMN posted_at timestamptz;",
		"ALTER TABLE public.report_items ADD COLUMN collected_at timestamptz;",
		"ALTER TABLE public.report_items ADD COLUMN recorded_at timestamptz;",
		"GRANT SELECT, INSERT ON TABLE public.report_items TO namo_runtime;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("migration 16 missing %q", want)
		}
	}
	if strings.Count(body, "GRANT ") != 1 {
		t.Fatalf("migration 16 must contain exactly one GRANT statement")
	}
	for _, forbidden := range []string{
		"NOT NULL",
		"DEFAULT",
		"UPDATE public.report_items",
		"INSERT INTO public.report_items",
		"demand_agency",
		"budget_amount",
		"GRANT UPDATE",
		"GRANT DELETE",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("migration 16 contains forbidden contract %q", forbidden)
		}
	}
}
