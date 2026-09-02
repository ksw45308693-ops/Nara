package migrations

import (
	"strings"
	"testing"
)

func TestAllNormalizesSQLLineEndings(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if strings.Contains(migration.SQL, "\r") {
			t.Fatalf("migration %d contains carriage returns", migration.Version)
		}
	}
}
