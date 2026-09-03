package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, cfg Config) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readConfig(t *testing.T, path string) Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPersistMigrate_WritesBlock(t *testing.T) {
	cfg := Config{Servers: []DatabaseServer{{
		Name: "old-pg", RootConnectionString: "postgres://r:p@old/postgres",
		Databases: []DatabaseConfig{{Database: "app", User: "app", Password: "pw"}},
	}}}
	path := writeTempConfig(t, cfg)

	persistMigrate(path, 0, 0, MigrateConfig{TargetServer: "new-pg", Error: "boom"})

	got := readConfig(t, path).Servers[0].Databases[0].Migrate
	if got == nil || got.TargetServer != "new-pg" || got.Error != "boom" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestPersistMigrate_OutOfRangeIsNoop(t *testing.T) {
	cfg := Config{Servers: []DatabaseServer{{Name: "s", Databases: []DatabaseConfig{{Database: "d"}}}}}
	path := writeTempConfig(t, cfg)
	persistMigrate(path, 5, 5, MigrateConfig{TargetServer: "x"}) // must not panic
	if readConfig(t, path).Servers[0].Databases[0].Migrate != nil {
		t.Error("expected no change")
	}
}

func TestRunMigration_UnknownTargetRecordsError(t *testing.T) {
	cfg := Config{Servers: []DatabaseServer{{
		Name: "old-pg", RootConnectionString: "postgres://r:p@old/postgres",
		Databases: []DatabaseConfig{{
			Database: "app", User: "app", Password: "pw",
			Migrate: &MigrateConfig{TargetServer: "ghost"},
		}},
	}}}
	path := writeTempConfig(t, cfg)

	runMigration(&cfg, 0, cfg.Servers[0], 0, cfg.Servers[0].Databases[0], path)

	got := readConfig(t, path).Servers[0].Databases[0].Migrate
	if got == nil || got.Completed || !strings.Contains(got.Error, "not found") {
		t.Fatalf("expected not-found error recorded, got %+v", got)
	}
}

func testConfig2PG() *Config {
	return &Config{Servers: []DatabaseServer{
		{Name: "old-pg", RootConnectionString: "postgres://r:p@old:5432/postgres"},
		{Name: "new-pg", RootConnectionString: "postgres://r:p@new:5432/postgres"},
		{Name: "maria", RootConnectionString: "mariadb://r:p@m:3306/mysql"},
	}}
}

func TestResolveTargetServer_OK(t *testing.T) {
	s, err := resolveTargetServer(testConfig2PG(), "new-pg", "old-pg")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.Name != "new-pg" {
		t.Errorf("got %q", s.Name)
	}
}

func TestResolveTargetServer_NotFound(t *testing.T) {
	if _, err := resolveTargetServer(testConfig2PG(), "ghost", "old-pg"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestResolveTargetServer_NotPostgreSQL(t *testing.T) {
	if _, err := resolveTargetServer(testConfig2PG(), "maria", "old-pg"); err == nil || !strings.Contains(err.Error(), "not PostgreSQL") {
		t.Fatalf("want not-PostgreSQL error, got %v", err)
	}
}

func TestResolveTargetServer_SameAsSource(t *testing.T) {
	if _, err := resolveTargetServer(testConfig2PG(), "old-pg", "old-pg"); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("want differ error, got %v", err)
	}
}

func TestIsConnectionError(t *testing.T) {
	for _, s := range []string{
		"pg_dump: error: connection to server failed: could not connect to server",
		"could not translate host name \"old\" to address",
		"dial tcp: connection refused",
	} {
		if !isConnectionError(s) {
			t.Errorf("expected connection error for %q", s)
		}
	}
	if isConnectionError("pg_dump: error: permission denied for table foo") {
		t.Error("permission error misclassified as connection error")
	}
}

func TestPgDumpError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("exit status 1")
	e := &pgDumpError{err: inner, stderr: "could not connect"}
	if !strings.Contains(e.Error(), "could not connect") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Error("expected Unwrap to expose inner error")
	}
}

func TestCompareTableCounts_Equal(t *testing.T) {
	a := map[string]int64{"public.users": 10, "public.orders": 3}
	b := map[string]int64{"public.orders": 3, "public.users": 10}
	if err := compareTableCounts(a, b); err != nil {
		t.Fatalf("expected equal, got %v", err)
	}
}

func TestCompareTableCounts_MissingTable(t *testing.T) {
	a := map[string]int64{"public.users": 10, "public.orders": 3}
	b := map[string]int64{"public.users": 10}
	if err := compareTableCounts(a, b); err == nil || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("want missing-table error, got %v", err)
	}
}

func TestCompareTableCounts_CountMismatch(t *testing.T) {
	a := map[string]int64{"public.users": 10}
	b := map[string]int64{"public.users": 9}
	if err := compareTableCounts(a, b); err == nil || !strings.Contains(err.Error(), "users") {
		t.Fatalf("want count-mismatch error, got %v", err)
	}
}
