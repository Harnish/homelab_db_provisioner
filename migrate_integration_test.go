//go:build integration

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/lib/pq"
)

// Requires two reachable Postgres instances. Set:
//   MIGRATE_SOURCE_CONN=postgres://postgres:rootpassword@localhost:5432/postgres?sslmode=disable
//   MIGRATE_TARGET_CONN=postgres://postgres:rootpassword@localhost:5433/postgres?sslmode=disable
// Run: go test -tags integration -run TestMigration_EndToEnd ./...
func TestMigration_EndToEnd(t *testing.T) {
	srcConn := os.Getenv("MIGRATE_SOURCE_CONN")
	tgtConn := os.Getenv("MIGRATE_TARGET_CONN")
	if srcConn == "" || tgtConn == "" {
		t.Skip("MIGRATE_SOURCE_CONN / MIGRATE_TARGET_CONN not set")
	}

	sdb, err := sql.Open("postgres", srcConn)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	sdb.Exec(`DROP DATABASE IF EXISTS mige2e WITH (FORCE)`)
	sdb.Exec(`DROP ROLE IF EXISTS mige2e`)
	if _, err := sdb.Exec(`CREATE ROLE mige2e LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`CREATE DATABASE mige2e OWNER mige2e`); err != nil {
		t.Fatal(err)
	}

	seedConn, err := connStrWithDB(srcConn, "mige2e")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sql.Open("postgres", seedConn)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if _, err := seed.Exec(`CREATE TABLE widgets (id int PRIMARY KEY); INSERT INTO widgets VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}

	tdb, _ := sql.Open("postgres", tgtConn)
	defer tdb.Close()
	tdb.Exec(`DROP DATABASE IF EXISTS mige2e WITH (FORCE)`)

	cfg := &Config{Servers: []DatabaseServer{
		{
			Name:                 "src",
			RootConnectionString: srcConn,
			Databases: []DatabaseConfig{{
				Database: "mige2e", User: "mige2e", Password: "pw",
				Migrate: &MigrateConfig{TargetServer: "tgt", ConfirmDrop: true},
			}},
		},
		{Name: "tgt", RootConnectionString: tgtConn, Databases: []DatabaseConfig{}},
	}}

	path := filepath.Join(t.TempDir(), "config.json")
	out, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(path, out, 0600)

	runMigration(cfg, 0, cfg.Servers[0], 0, cfg.Servers[0].Databases[0], path)

	var got Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &got)
	m := got.Servers[0].Databases[0].Migrate
	if m == nil || !m.Completed || m.Error != "" {
		t.Fatalf("migration not completed cleanly: %+v", m)
	}

	tgtDBConn, _ := connStrWithDB(tgtConn, "mige2e")
	vdb, err := sql.Open("postgres", tgtDBConn)
	if err != nil {
		t.Fatal(err)
	}
	defer vdb.Close()
	var n int
	if err := vdb.QueryRow(`SELECT count(*) FROM widgets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 rows on target, got %d", n)
	}

	var exists bool
	sdb.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='mige2e')`).Scan(&exists)
	if exists {
		t.Fatal("expected source database to be dropped")
	}
}
