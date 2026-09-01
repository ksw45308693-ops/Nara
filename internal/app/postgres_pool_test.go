package app

import (
	"testing"
	"time"
)

func TestRuntimePoolConfigInstallsRoleCheckAndBoundsConnections(t *testing.T) {
	config, err := runtimePoolConfig("postgres://g2b_app:secret@127.0.0.1/g2b_monitor?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if config.AfterConnect == nil || config.MaxConns != 8 || config.MinConns != 1 {
		t.Fatalf("pool bounds/check = max:%d min:%d check:%t", config.MaxConns, config.MinConns, config.AfterConnect != nil)
	}
	if config.MaxConnLifetime < 10*time.Minute || config.HealthCheckPeriod <= 0 {
		t.Fatalf("pool lifecycle = lifetime:%s health:%s", config.MaxConnLifetime, config.HealthCheckPeriod)
	}
	if got := config.ConnConfig.RuntimeParams["search_path"]; got != "pg_catalog,public" {
		t.Fatalf("runtime search_path=%q, want pg_catalog,public", got)
	}
}
