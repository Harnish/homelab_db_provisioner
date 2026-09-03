# PostgreSQL Database Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the provisioner move a PostgreSQL database from one configured server to another (major-version upgrade) via a `pg_dump | psql` cutover, driven by a `migrate` config block or an admin-UI button.

**Architecture:** A new `migrate` block on a `DatabaseConfig` is picked up by a dedicated migration pass at the top of `processConfig` (before normal provisioning, so the backup fallback works even when the source is down). `runMigration` prepares the target with the existing `provisionPostgreSQL`, streams `pg_dump`(source) → `psql`(target), verifies by comparing per-table row counts, then marks `completed` and optionally drops the source database. All logic lives in a new `migrate.go`.

**Tech Stack:** Go (stdlib `database/sql`, `os/exec`, `net/url`, `encoding/json`), `github.com/lib/pq` driver, `pg_dump`/`psql` from the alpine image's `postgresql18-client`. Tests: stdlib `testing` + `net/http/httptest`, table-driven, no frameworks.

**Spec:** `docs/superpowers/specs/2026-09-01-postgresql-database-migration-design.md`

## Global Constraints

- Package is `package main`, all files in the repo root. No new Go dependencies.
- Config file writes: `json.MarshalIndent(cfg, "", "  ")` then `os.WriteFile(path, out, 0600)`, always under `configMu.Lock()`, always re-reading the file from disk first (never write back an in-memory `Config` that may be stale).
- Migration is **PostgreSQL only**. MariaDB/MongoDB entries ignore any `migrate` block.
- `runMigration` never returns a fatal error — a failed migration must not stop the rest of `processConfig`. Every failure path records `Migrate.Error`, persists, logs, and returns.
- Identifiers in SQL string interpolation use the existing `quoteIdentifier`.
- Commit after every task with a `feat:` / `refactor:` / `test:` / `docs:` conventional message. Do not push.
- Run `go test ./...` and `go build ./...` before every commit; both must pass.

---

## File Structure

- `main.go` — add `MigrateConfig` type + `DatabaseConfig.Migrate` field; add the migration pass and tombstone skip inside `processConfig`.
- `backup.go` — extract `connStrWithDB`; refactor `backupPostgreSQL` / `restorePostgreSQL`; add tombstone skip to `runBackups`.
- `migrate.go` — **new**, ~250 lines, owns everything migration-specific.
- `migrate_test.go` — **new**, unit tests for the pure helpers + `persistMigrate` + `runMigration` failure paths.
- `migrate_integration_test.go` — **new**, `//go:build integration`, end-to-end against two real Postgres instances.
- `admin.go` — `handleMigrateDatabase` + route + `adminTemplateData.PGServerNames` + `handleIndex` + template row control.
- `admin_test.go` — handler + template-render tests.
- `main_test.go` — `MigrateConfig` JSON round-trip; `processConfig` tombstone skip.
- `backup_test.go` — `connStrWithDB` test; `runBackups` tombstone skip test.
- `CLAUDE.md` — "Database migration" subsection.
- `docker-compose.yml`, `config-multi.json` — second postgres service + integration config.

---

## Task 1: `MigrateConfig` type and config field

**Files:**
- Modify: `main.go` (after the `BackupConfig` struct, ~line 27; and `DatabaseConfig`, ~line 36-44)
- Test: `main_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  type MigrateConfig struct {
      TargetServer string `json:"target_server"`
      ConfirmDrop  bool   `json:"confirm_drop,omitempty"`
      Completed    bool   `json:"completed,omitempty"`
      CompletedAt  string `json:"completed_at,omitempty"`
      Error        string `json:"error,omitempty"`
  }
  ```
  and new field on `DatabaseConfig`: `Migrate *MigrateConfig `json:"migrate,omitempty"``

- [ ] **Step 1: Write the failing test**

In `main_test.go`:

```go
func TestConfig_MigrateConfigRoundTrip(t *testing.T) {
	raw := `{
		"servers": [
			{
				"name": "old-pg",
				"root_connection_string": "postgres://root:pass@old:5432/postgres",
				"databases": [
					{
						"database": "app",
						"user": "app",
						"password": "pw",
						"migrate": {
							"target_server": "new-pg",
							"confirm_drop": true,
							"completed": true,
							"completed_at": "2026-09-03T12:00:00Z",
							"error": ""
						}
					}
				]
			}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := cfg.Servers[0].Databases[0].Migrate
	if m == nil {
		t.Fatal("expected Migrate to be non-nil")
	}
	if m.TargetServer != "new-pg" || !m.ConfirmDrop || !m.Completed || m.CompletedAt != "2026-09-03T12:00:00Z" {
		t.Errorf("unexpected Migrate: %+v", m)
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"target_server":"new-pg"`) {
		t.Errorf("marshalled config missing migrate block: %s", out)
	}
}

func TestConfig_MigrateConfigAbsent(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"servers":[{"name":"x","root_connection_string":"postgres://x","databases":[{"database":"d","user":"u","password":"p"}]}]}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Servers[0].Databases[0].Migrate != nil {
		t.Error("expected Migrate to be nil")
	}
	out, _ := json.Marshal(cfg)
	if strings.Contains(string(out), "migrate") {
		t.Errorf("absent migrate should be omitted: %s", out)
	}
}
```

`main_test.go` already imports `encoding/json` and `strings`? Check imports; add `strings` if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestConfig_MigrateConfig ./...`
Expected: FAIL — compile error, `Migrate` field and `MigrateConfig` undefined.

- [ ] **Step 3: Add the type and field**

In `main.go`, immediately after the `BackupConfig` struct:

```go
type MigrateConfig struct {
	TargetServer string `json:"target_server"`          // Name of a DatabaseServer already present in config.Servers
	ConfirmDrop  bool   `json:"confirm_drop,omitempty"`  // drop the database on the source server after a verified restore
	Completed    bool   `json:"completed,omitempty"`     // set true by the provisioner on success
	CompletedAt  string `json:"completed_at,omitempty"`  // RFC3339 timestamp, set on success
	Error        string `json:"error,omitempty"`         // last failure message; cleared on success
}
```

In `DatabaseConfig`, add as the last field:

```go
	Migrate *MigrateConfig `json:"migrate,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run TestConfig_MigrateConfig ./... && go build ./...`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add MigrateConfig type and DatabaseConfig.Migrate field"
```

---

## Task 2: Extract `connStrWithDB` helper

**Files:**
- Modify: `backup.go` — `backupPostgreSQL` (~L108-137), `restorePostgreSQL` (~L272-300)
- Test: `backup_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func connStrWithDB(rootConnStr, database string) (string, error)` — parses `rootConnStr` as a URL, sets its path to `/<database>`, returns the string form. Error wraps `"parse connection string: %w"`.

- [ ] **Step 1: Write the failing test**

In `backup_test.go`:

```go
func TestConnStrWithDB(t *testing.T) {
	got, err := connStrWithDB("postgres://root:pass@host:5432/postgres", "app")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "postgres://root:pass@host:5432/app" {
		t.Errorf("got %q", got)
	}
}

func TestConnStrWithDB_BadURL(t *testing.T) {
	if _, err := connStrWithDB("://not a url", "app"); err == nil {
		t.Fatal("expected error for malformed connection string")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestConnStrWithDB ./...`
Expected: FAIL — `connStrWithDB` undefined.

- [ ] **Step 3: Add the helper and refactor the two callers**

In `backup.go`, add near the top (after imports):

```go
// connStrWithDB rewrites a root connection URL to point at a specific database.
func connStrWithDB(rootConnStr, database string) (string, error) {
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return "", fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/" + database
	return u.String(), nil
}
```

In `backupPostgreSQL`, replace:

```go
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/" + database
	connStr := u.String()
```

with:

```go
	connStr, err := connStrWithDB(rootConnStr, database)
	if err != nil {
		return err
	}
```

Do the identical replacement in `restorePostgreSQL`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS (all existing backup tests still green).

- [ ] **Step 5: Commit**

```bash
git add backup.go backup_test.go
git commit -m "refactor: extract connStrWithDB helper from backup functions"
```

---

## Task 3: Pure migration helpers — `resolveTargetServer`, `isConnectionError`, `compareTableCounts`

**Files:**
- Create: `migrate.go`
- Create: `migrate_test.go`

**Interfaces:**
- Consumes: `Config`, `DatabaseServer`, `detectDBType`, `PostgreSQL` (all from `main.go`).
- Produces:
  - `func resolveTargetServer(config *Config, targetName, sourceName string) (DatabaseServer, error)` — returns the named server; errors: `"target server %q not found"`, `"target server %q is not PostgreSQL"`, `"target server must differ from source"`.
  - `func isConnectionError(stderr string) bool` — true if stderr contains any of: `could not connect`, `connection refused`, `could not translate host name`, `no such host`, `Connection timed out`, `could not connect to server`.
  - `func compareTableCounts(source, target map[string]int64) error` — nil if identical key sets and values; else an error describing the first discrepancy (missing key or count mismatch), keys sorted for determinism.

- [ ] **Step 1: Write the failing tests**

`migrate.go` (stub so the file exists):

```go
package main
```

`migrate_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestResolveTargetServer|TestIsConnectionError|TestCompareTableCounts' ./...`
Expected: FAIL — helpers undefined.

- [ ] **Step 3: Implement the helpers**

Replace `migrate.go` contents:

```go
package main

import (
	"fmt"
	"sort"
	"strings"
)

func resolveTargetServer(config *Config, targetName, sourceName string) (DatabaseServer, error) {
	if targetName == sourceName {
		return DatabaseServer{}, fmt.Errorf("target server must differ from source")
	}
	for _, s := range config.Servers {
		if s.Name != targetName {
			continue
		}
		if detectDBType(s.RootConnectionString) != PostgreSQL {
			return DatabaseServer{}, fmt.Errorf("target server %q is not PostgreSQL", targetName)
		}
		return s, nil
	}
	return DatabaseServer{}, fmt.Errorf("target server %q not found", targetName)
}

var connErrMarkers = []string{
	"could not connect",
	"could not connect to server",
	"connection refused",
	"could not translate host name",
	"no such host",
	"connection timed out",
}

func isConnectionError(stderr string) bool {
	low := strings.ToLower(stderr)
	for _, m := range connErrMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// compareTableCounts returns nil only if both maps have identical keys and values.
func compareTableCounts(source, target map[string]int64) error {
	keys := make([]string, 0, len(source))
	for k := range source {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tv, ok := target[k]
		if !ok {
			return fmt.Errorf("table %s present on source but missing on target", k)
		}
		if tv != source[k] {
			return fmt.Errorf("table %s row count mismatch: source=%d target=%d", k, source[k], tv)
		}
	}
	extra := make([]string, 0)
	for k := range target {
		if _, ok := source[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("tables present on target but missing on source: %s", strings.Join(extra, ", "))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrate.go migrate_test.go
git commit -m "feat: add pure migration helpers (resolve target, conn-error, count compare)"
```

---

## Task 4: DB-touching migration primitives — dump/restore stream, drop, verify

**Files:**
- Modify: `migrate.go`
- Modify: `migrate_test.go` (only the connstr-rewrite reuse is unit-tested here; DB behaviour is covered by the integration test in Task 9)

**Interfaces:**
- Consumes: `connStrWithDB` (backup.go), `quoteIdentifier` (main.go), `sql.Open` with `"postgres"`.
- Produces:
  - `type pgDumpError struct { err error; stderr string }` implementing `error` (`Error()` returns `fmt.Sprintf("pg_dump: %v: %s", e.err, e.stderr)`), plus `Unwrap() error`.
  - `func migratePostgreSQLData(sourceConnStr, targetConnStr, database string) error` — streams `pg_dump | psql`. Returns `*pgDumpError` when `pg_dump` fails; logs (does not fail on) `psql` non-zero exit.
  - `func dropPostgreSQLDatabase(rootConnStr, database string) error` — `DROP DATABASE <quoted> WITH (FORCE)` on the root connection.
  - `func queryTableCounts(connStr string) (map[string]int64, error)` — connects, lists base tables (excluding `pg_catalog`, `information_schema`), returns `{"schema.table": count}`.
  - `func verifyMigration(sourceConnStr, targetConnStr, database string) error` — `connStrWithDB` both, `queryTableCounts` both, `compareTableCounts`.
  - `func verifyTargetNonEmpty(targetConnStr, database string) error` — `queryTableCounts` on target only; error if zero tables.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go`:

```go
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
```

Add imports `errors`, `fmt` to `migrate_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestPgDumpError ./...`
Expected: FAIL — `pgDumpError` undefined.

- [ ] **Step 3: Implement the primitives**

Add to `migrate.go` (add imports: `context`, `database/sql`, `log`, `os/exec`):

```go
type pgDumpError struct {
	err    error
	stderr string
}

func (e *pgDumpError) Error() string { return fmt.Sprintf("pg_dump: %v: %s", e.err, e.stderr) }
func (e *pgDumpError) Unwrap() error { return e.err }

// migratePostgreSQLData streams pg_dump (source) directly into psql (target).
// A pg_dump failure is returned as *pgDumpError. psql errors are logged but not
// returned — verification is the correctness gate.
func migratePostgreSQLData(sourceConnStr, targetConnStr, database string) error {
	src, err := connStrWithDB(sourceConnStr, database)
	if err != nil {
		return err
	}
	tgt, err := connStrWithDB(targetConnStr, database)
	if err != nil {
		return err
	}

	dump := exec.Command("pg_dump", "--no-password", src)
	restore := exec.Command("psql", "--no-password", "--set", "ON_ERROR_STOP=0", tgt)

	pipe, err := dump.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	restore.Stdin = pipe

	var dumpErr, restoreErr strings.Builder
	dump.Stderr = &dumpErr
	restore.Stderr = &restoreErr

	if err := restore.Start(); err != nil {
		return fmt.Errorf("start psql: %w", err)
	}
	if err := dump.Run(); err != nil {
		if restore.Process != nil {
			_ = restore.Process.Kill()
		}
		_ = restore.Wait()
		return &pgDumpError{err: err, stderr: dumpErr.String()}
	}
	if err := restore.Wait(); err != nil {
		log.Printf("migrate: psql reported errors (non-fatal, verification decides): %v: %s", err, restoreErr.String())
	}
	return nil
}

func dropPostgreSQLDatabase(rootConnStr, database string) error {
	conn, err := sql.Open("postgres", rootConnStr)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer conn.Close()
	stmt := fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", quoteIdentifier(database))
	if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

const baseTableQuery = `
SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'
  AND table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY 1, 2`

func queryTableCounts(connStr string) (map[string]int64, error) {
	conn, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer conn.Close()
	ctx := context.Background()

	rows, err := conn.QueryContext(ctx, baseTableQuery)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	type tbl struct{ schema, name string }
	var tables []tbl
	for rows.Next() {
		var s, n string
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, tbl{s, n})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(tables))
	for _, tb := range tables {
		q := fmt.Sprintf("SELECT count(*) FROM %s.%s", quoteIdentifier(tb.schema), quoteIdentifier(tb.name))
		var c int64
		if err := conn.QueryRowContext(ctx, q).Scan(&c); err != nil {
			return nil, fmt.Errorf("count %s.%s: %w", tb.schema, tb.name, err)
		}
		counts[tb.schema+"."+tb.name] = c
	}
	return counts, nil
}

func verifyMigration(sourceConnStr, targetConnStr, database string) error {
	src, err := connStrWithDB(sourceConnStr, database)
	if err != nil {
		return err
	}
	tgt, err := connStrWithDB(targetConnStr, database)
	if err != nil {
		return err
	}
	sourceCounts, err := queryTableCounts(src)
	if err != nil {
		return fmt.Errorf("read source counts: %w", err)
	}
	targetCounts, err := queryTableCounts(tgt)
	if err != nil {
		return fmt.Errorf("read target counts: %w", err)
	}
	return compareTableCounts(sourceCounts, targetCounts)
}

func verifyTargetNonEmpty(targetConnStr, database string) error {
	tgt, err := connStrWithDB(targetConnStr, database)
	if err != nil {
		return err
	}
	counts, err := queryTableCounts(tgt)
	if err != nil {
		return fmt.Errorf("read target counts: %w", err)
	}
	if len(counts) == 0 {
		return fmt.Errorf("target database %q has no tables after restore", database)
	}
	return nil
}
```

`// ponytail: O(tables) exact count() queries, full scan each. Fine for homelab DB sizes; swap to pg_class.reltuples estimates if this gets slow.` — put this comment above `queryTableCounts`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrate.go migrate_test.go
git commit -m "feat: add pg_dump|psql stream, source drop, and row-count verification"
```

---

## Task 5: `persistMigrate` and `runMigration` orchestration

**Files:**
- Modify: `migrate.go`
- Modify: `migrate_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3-4; `provisionPostgreSQL` + `checkPostgreSQLDatabaseExists` (main.go); `findNewestBackup` + `restorePostgreSQL` (backup.go); `configMu` (main.go).
- Produces:
  - `func persistMigrate(configPath string, serverIdx, dbIdx int, m MigrateConfig)` — re-reads config under `configMu.Lock()`, sets `Servers[serverIdx].Databases[dbIdx].Migrate = &m`, writes `0600`. All errors logged, never returned. No-op if indices out of range.
  - `func runMigration(config *Config, serverIdx int, source DatabaseServer, dbIdx int, db DatabaseConfig, configPath string)` — full orchestration per the spec. Returns nothing.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go` (add imports `encoding/json`, `os`, `path/filepath`):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestPersistMigrate|TestRunMigration' ./...`
Expected: FAIL — `persistMigrate` / `runMigration` undefined.

- [ ] **Step 3: Implement**

Add to `migrate.go` (add imports: `encoding/json`, `os`, `time`, `errors`):

```go
func persistMigrate(configPath string, serverIdx, dbIdx int, m MigrateConfig) {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("migrate: persist read %s: %v", configPath, err)
		return
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("migrate: persist parse: %v", err)
		return
	}
	if serverIdx < 0 || serverIdx >= len(cfg.Servers) {
		return
	}
	if dbIdx < 0 || dbIdx >= len(cfg.Servers[serverIdx].Databases) {
		return
	}
	mc := m
	cfg.Servers[serverIdx].Databases[dbIdx].Migrate = &mc

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Printf("migrate: persist marshal: %v", err)
		return
	}
	if err := os.WriteFile(configPath, out, 0600); err != nil {
		log.Printf("migrate: persist write: %v", err)
	}
}

func runMigration(config *Config, serverIdx int, source DatabaseServer, dbIdx int, db DatabaseConfig, configPath string) {
	m := *db.Migrate
	fail := func(format string, args ...interface{}) {
		m.Completed = false
		m.Error = fmt.Sprintf(format, args...)
		log.Printf("migrate: %s/%s: %s", source.Name, db.Database, m.Error)
		persistMigrate(configPath, serverIdx, dbIdx, m)
	}

	target, err := resolveTargetServer(config, m.TargetServer, source.Name)
	if err != nil {
		fail("%v", err)
		return
	}
	log.Printf("migrate: %s/%s -> %s starting", source.Name, db.Database, target.Name)

	// Prepare target.
	targetRoot, err := sql.Open("postgres", target.RootConnectionString)
	if err != nil {
		fail("open target: %v", err)
		return
	}
	exists, err := checkPostgreSQLDatabaseExists(context.Background(), targetRoot, db.Database)
	if err != nil {
		targetRoot.Close()
		fail("check target database: %v", err)
		return
	}
	if exists {
		targetRoot.Close()
		fail("target database %q already exists on %q, refusing to overwrite", db.Database, target.Name)
		return
	}
	if _, err := provisionPostgreSQL(targetRoot, target.RootConnectionString, db, false); err != nil {
		targetRoot.Close()
		fail("provision target: %v", err)
		return
	}
	targetRoot.Close()

	// Dump + restore.
	fromBackup := false
	if dumpErr := migratePostgreSQLData(source.RootConnectionString, target.RootConnectionString, db.Database); dumpErr != nil {
		var de *pgDumpError
		if errors.As(dumpErr, &de) && isConnectionError(de.stderr) {
			log.Printf("migrate: %s/%s: source unreachable, falling back to newest backup", source.Name, db.Database)
			backupFile := findNewestBackup(config, configPath, source.Name, db.Database, PostgreSQL)
			if backupFile == "" {
				fail("source unreachable and no backup found")
				return
			}
			if err := restorePostgreSQL(target.RootConnectionString, db.Database, backupFile); err != nil {
				fail("restore from backup %s: %v", backupFile, err)
				return
			}
			fromBackup = true
		} else {
			fail("%v", dumpErr)
			return
		}
	}

	// Verify.
	if fromBackup {
		if err := verifyTargetNonEmpty(target.RootConnectionString, db.Database); err != nil {
			fail("verification (backup fallback) failed: %v", err)
			return
		}
		log.Printf("migrate: %s/%s: restored from backup; full source/target verification skipped", source.Name, db.Database)
	} else {
		if err := verifyMigration(source.RootConnectionString, target.RootConnectionString, db.Database); err != nil {
			fail("verification failed: %v", err)
			return
		}
	}

	// Success.
	m.Completed = true
	m.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	m.Error = ""

	if m.ConfirmDrop {
		if err := dropPostgreSQLDatabase(source.RootConnectionString, db.Database); err != nil {
			m.Error = fmt.Sprintf("migration succeeded but source drop failed: %v", err)
			log.Printf("migrate: %s/%s: %s", source.Name, db.Database, m.Error)
		} else {
			log.Printf("migrate: %s/%s: dropped source database", source.Name, db.Database)
		}
	}

	persistMigrate(configPath, serverIdx, dbIdx, m)
	log.Printf("migrate: %s/%s -> %s complete", source.Name, db.Database, target.Name)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS. (`TestRunMigration_UnknownTargetRecordsError` exercises the early-exit path with no DB.)

- [ ] **Step 5: Commit**

```bash
git add migrate.go migrate_test.go
git commit -m "feat: add persistMigrate and runMigration orchestration"
```

---

## Task 6: Wire migration into `processConfig` and skip tombstones in provisioning + backups

**Files:**
- Modify: `main.go` — `processConfig` (~L196-381)
- Modify: `backup.go` — `runBackups` (~L61-64)
- Test: `main_test.go`, `backup_test.go`

**Interfaces:**
- Consumes: `runMigration` (migrate.go).
- Produces: no new exported symbols. Behaviour: a `migrate` block with `completed:false` triggers `runMigration` before provisioning; `completed:true` entries are skipped by both `processConfig`'s provisioning loop and `runBackups`.

- [ ] **Step 1: Write the failing tests**

In `main_test.go` (add imports as needed: `os`, `encoding/json`, `path/filepath`):

```go
func TestProcessConfig_CompletedMigrationSkipsProvisioning(t *testing.T) {
	// Source conn string is unroutable; if provisioning were attempted the
	// connect-with-retry would still just log and continue, so we assert on the
	// migration pass NOT re-running (Completed stays true, no Error added) and
	// that processConfig returns nil.
	cfg := &Config{Servers: []DatabaseServer{{
		Name:                 "old-pg",
		RootConnectionString: "postgres://r:p@127.0.0.1:1/postgres",
		Databases: []DatabaseConfig{{
			Database: "app", User: "app", Password: "pw",
			Migrate: &MigrateConfig{TargetServer: "new-pg", Completed: true, CompletedAt: "2026-09-03T00:00:00Z"},
		}},
	}}}

	if err := processConfig(cfg); err != nil {
		t.Fatalf("processConfig returned error: %v", err)
	}
	m := cfg.Servers[0].Databases[0].Migrate
	if m == nil || !m.Completed || m.Error != "" {
		t.Fatalf("completed migration should be untouched, got %+v", m)
	}
}

func TestRunBackups_SkipsCompletedMigration(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Servers: []DatabaseServer{{
		Name:                 "old-pg",
		RootConnectionString: "postgres://r:p@127.0.0.1:1/postgres",
		Databases: []DatabaseConfig{{
			Database: "app", User: "app", Password: "pw",
			Backup:  &BackupConfig{Enabled: true, Schedule: "daily"},
			Migrate: &MigrateConfig{TargetServer: "new-pg", Completed: true},
		}},
	}}}

	runBackups(cfg, configPath, time.Now())

	// A tombstoned entry must not create a backup directory/file.
	if _, err := os.Stat(filepath.Join(dir, "backups", "old-pg", "app")); !os.IsNotExist(err) {
		t.Fatalf("expected no backup dir for tombstoned entry, stat err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestProcessConfig_CompletedMigration|TestRunBackups_SkipsCompleted' ./...`
Expected: `TestRunBackups_SkipsCompletedMigration` FAILS (backup dir created / pg_dump attempted). `TestProcessConfig_CompletedMigrationSkipsProvisioning` may already pass depending on timing — that's fine, it's a regression guard.

- [ ] **Step 3: Implement**

In `main.go` `processConfig`, immediately after the backup-summary block ends (after the `log.Println("========================================")` that precedes `// Process each server`), insert the migration pass:

```go
	// Migration pass — runs before normal provisioning so the backup fallback
	// works even when the source server's root connection is dead.
	for si, server := range config.Servers {
		if detectDBType(server.RootConnectionString) != PostgreSQL {
			continue
		}
		for di, dbConfig := range server.Databases {
			if dbConfig.Migrate == nil || dbConfig.Migrate.Completed {
				continue
			}
			if server.DryRun {
				log.Printf("[DRY RUN] would migrate %s/%s to %s (confirm_drop=%v)",
					server.Name, dbConfig.Database, dbConfig.Migrate.TargetServer, dbConfig.Migrate.ConfirmDrop)
				continue
			}
			runMigration(config, si, server, di, dbConfig, getConfigPath())
		}
	}
```

Then, in the per-database provisioning loop (`for i, dbConfig := range server.Databases {`), as the first statement inside the loop body:

```go
			if dbConfig.Migrate != nil && dbConfig.Migrate.Completed {
				log.Printf("migrate: %s/%s migrated to %s, skipping provisioning and backups",
					serverName, dbConfig.Database, dbConfig.Migrate.TargetServer)
				continue
			}
```

Apply the **same** skip inside the MongoDB branch's database loop (`main.go` ~L280's `for _, dbConfig := range server.Databases` — but MongoDB servers never match the migration pass's PostgreSQL filter, and `Migrate` is only ever set on PG entries; still, add the guard there too for symmetry and safety). If the MongoDB loop uses `_` for the index, keep `_`.

In `backup.go` `runBackups`, change:

```go
			if db.Backup == nil || !db.Backup.Enabled {
				continue
			}
```

to:

```go
			if db.Migrate != nil && db.Migrate.Completed {
				continue
			}
			if db.Backup == nil || !db.Backup.Enabled {
				continue
			}
```

Apply the same two-line guard in `mongo.go`'s backup loop (~L173) for symmetry.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go backup.go mongo.go main_test.go backup_test.go
git commit -m "feat: run migration pass in processConfig, skip completed tombstones"
```

---

## Task 7: Admin handler `handleMigrateDatabase`

**Files:**
- Modify: `admin.go` — `newAdminHandler` (~L179-199), new handler function near `handleAddDatabase`
- Test: `admin_test.go`

**Interfaces:**
- Consumes: `resolveTargetServer` (migrate.go), `configMu`, `detectDBType`, `PostgreSQL`.
- Produces: `func handleMigrateDatabase(configPath string) http.HandlerFunc`. Route: `mux.HandleFunc("/migrate-database", handleMigrateDatabase(configPath))`.
  - POST only (else 405).
  - Form fields: `server_index`, `db_index` (both required ints), `target_server` (string), `confirm_drop` (`"on"`).
  - Empty `target_server` → set `Migrate = nil` (clear/retry).
  - Non-empty → validate via `resolveTargetServer(&cfg, target, sourceName)`; on error redirect `/?msg=Error: <err>`. On success set `Migrate = &MigrateConfig{TargetServer: target, ConfirmDrop: confirmDrop}`.
  - Out-of-range indices → `http.Error(..., 400)`.
  - Writes config `0600`, redirects `/?msg=...`.

- [ ] **Step 1: Write the failing tests**

In `admin_test.go`:

```go
func TestMigrateDatabase_SetsBlock(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[
		{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]},
		{"name":"new-pg","root_connection_string":"postgres://r:p@new/postgres","databases":[]}
	]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":  {"0"},
		"db_index":      {"0"},
		"target_server": {"new-pg"},
		"confirm_drop":  {"on"},
	}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	m := out.Servers[0].Databases[0].Migrate
	if m == nil || m.TargetServer != "new-pg" || !m.ConfirmDrop {
		t.Fatalf("unexpected migrate block: %+v", m)
	}
}

func TestMigrateDatabase_EmptyTargetClears(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw","migrate":{"target_server":"new-pg","error":"boom"}}]}]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}, "target_server": {""}}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	if out.Servers[0].Databases[0].Migrate != nil {
		t.Fatalf("expected migrate cleared, got %+v", out.Servers[0].Databases[0].Migrate)
	}
}

func TestMigrateDatabase_RejectsSameServer(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]}]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}, "target_server": {"old-pg"}}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "Error") {
		t.Fatalf("expected error flash, got %q", loc)
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	if out.Servers[0].Databases[0].Migrate != nil {
		t.Fatal("migrate block should not have been written")
	}
}

func TestMigrateDatabase_WrongMethod(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)
	req := httptest.NewRequest("GET", "/migrate-database", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestMigrateDatabase ./...`
Expected: FAIL — route 404s / handler undefined.

- [ ] **Step 3: Implement**

In `admin.go` `newAdminHandler`, add after the `/add-server` line:

```go
	mux.HandleFunc("/migrate-database", handleMigrateDatabase(configPath))
```

Add the handler (model it on `handleUpdatePassword` for index parsing + range checks):

```go
func handleMigrateDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}
		targetServer := strings.TrimSpace(r.FormValue("target_server"))
		confirmDrop := r.FormValue("confirm_drop") == "on"

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}

		if targetServer == "" {
			cfg.Servers[si].Databases[di].Migrate = nil
		} else {
			if _, err := resolveTargetServer(&cfg, targetServer, cfg.Servers[si].Name); err != nil {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: "+err.Error()), http.StatusSeeOther)
				return
			}
			cfg.Servers[si].Databases[di].Migrate = &MigrateConfig{
				TargetServer: targetServer,
				ConfirmDrop:  confirmDrop,
			}
		}

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		msg := "Migration scheduled"
		if targetServer == "" {
			msg = "Migration cleared"
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add /migrate-database admin handler"
```

---

## Task 8: Admin template migration control

**Files:**
- Modify: `admin.go` — `adminTemplateData` (~L171-177), `adminTemplate` (~L27-169), `handleIndex` (~L228-260)
- Test: `admin_test.go`

**Interfaces:**
- Consumes: `adminTemplateData`, `detectDBType`, `PostgreSQL`.
- Produces: `adminTemplateData` gains `PGServerNames []string`. Template renders, per database row, either a "Migrate" form (`Migrate == nil`) or a status badge.

- [ ] **Step 1: Write the failing tests**

In `admin_test.go`:

```go
func TestIndex_ShowsMigrateFormForPostgres(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[
		{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]},
		{"name":"new-pg","root_connection_string":"postgres://r:p@new/postgres","databases":[]}
	]}`
	h := newAdminHandler(makeTestConfig(t, cfg))
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `action="/migrate-database"`) {
		t.Error("expected migrate form")
	}
	if !strings.Contains(body, `value="new-pg"`) {
		t.Error("expected new-pg as a migrate target option")
	}
}

func TestIndex_ShowsMigrateStatusBadge(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw","migrate":{"target_server":"new-pg","completed":true,"completed_at":"2026-09-03T12:00:00Z"}}]}]}`
	h := newAdminHandler(makeTestConfig(t, cfg))
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "migrated to new-pg") {
		t.Errorf("expected completed badge, body:\n%s", body)
	}
	if strings.Contains(body, `action="/migrate-database"`) && !strings.Contains(body, "Clear") {
		t.Error("completed row should offer a Clear button, not a fresh form")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestIndex_ShowsMigrate' ./...`
Expected: FAIL — no migrate markup rendered.

- [ ] **Step 3: Implement**

In `adminTemplateData`, add:

```go
	PGServerNames []string
```

In `handleIndex`, after building `tmplData` (before `adminTemplate.Execute`):

```go
		for _, s := range cfg.Servers {
			if detectDBType(s.RootConnectionString) == PostgreSQL {
				tmplData.PGServerNames = append(tmplData.PGServerNames, s.Name)
			}
		}
```

In `adminTemplate`: add a `Migrate` column header and cell. Add to the header row (after the `Backup` `<th>`):

```html
<th>Migrate</th>
```

Add this `<td>` after the Backup `<td>` in the `{{range $di, $db := $server.Databases}}` row (only meaningful for PostgreSQL servers; for non-PG servers render a dash):

```html
        <td>
          {{if ne (dbType $server.RootConnectionString) "postgres"}}—
          {{else if not $db.Migrate}}
            <form method="POST" action="/migrate-database" style="display:inline-flex;gap:0.25rem;align-items:center;flex-wrap:wrap;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <select name="target_server">
                <option value="">— target —</option>
                {{range $.PGServerNames}}{{if ne . $server.Name}}<option value="{{.}}">{{.}}</option>{{end}}{{end}}
              </select>
              <label style="display:inline;margin:0;"><input type="checkbox" name="confirm_drop"> drop source</label>
              <button type="submit">Migrate</button>
            </form>
          {{else if $db.Migrate.Completed}}
            <span>migrated to {{$db.Migrate.TargetServer}} at {{$db.Migrate.CompletedAt}}</span>
            {{if $db.Migrate.Error}}<br><small class="flash-err">{{$db.Migrate.Error}}</small>{{end}}
            <form method="POST" action="/migrate-database" style="display:inline;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="target_server" value="">
              <button type="submit">Clear</button>
            </form>
          {{else if $db.Migrate.Error}}
            <span class="flash-err">migration error: {{$db.Migrate.Error}}</span>
            <form method="POST" action="/migrate-database" style="display:inline;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="target_server" value="">
              <button type="submit">Retry (clear)</button>
            </form>
          {{else}}
            <span>migrating to {{$db.Migrate.TargetServer}}…</span>
          {{end}}
        </td>
```

Register the `dbType` template function. In the `template.FuncMap` at `adminTemplate` definition, add:

```go
	"dbType": func(connStr string) string {
		switch detectDBType(connStr) {
		case MariaDB:
			return "mariadb"
		case MongoDB:
			return "mongodb"
		default:
			return "postgres"
		}
	},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go build ./...`
Expected: PASS. If a pre-existing template test asserts an exact column count / row HTML, update it to expect the new `Migrate` column.

- [ ] **Step 5: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: render migration control in admin UI database rows"
```

---

## Task 9: Docs + integration test + docker-compose target

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docker-compose.yml`, `config-multi.json`
- Create: `migrate_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `runMigration`, `Config`, `processConfig`.
- Produces: nothing importable.

- [ ] **Step 1: Write the integration test**

`migrate_integration_test.go`:

```go
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
//   MIGRATE_SOURCE_CONN=postgres://postgres:postgres@localhost:5432/postgres
//   MIGRATE_TARGET_CONN=postgres://postgres:postgres@localhost:5433/postgres
// Run: go test -tags integration -run TestMigration_EndToEnd ./...
func TestMigration_EndToEnd(t *testing.T) {
	srcConn := os.Getenv("MIGRATE_SOURCE_CONN")
	tgtConn := os.Getenv("MIGRATE_TARGET_CONN")
	if srcConn == "" || tgtConn == "" {
		t.Skip("MIGRATE_SOURCE_CONN / MIGRATE_TARGET_CONN not set")
	}

	// Seed source: create db + role + a table with 3 rows.
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
	seedConn := srcConn[:len(srcConn)-len("/postgres")] + "/mige2e"
	seed, err := sql.Open("postgres", seedConn)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if _, err := seed.Exec(`CREATE TABLE widgets (id int PRIMARY KEY); INSERT INTO widgets VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}

	// Ensure target is clean.
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
	t.Setenv("CONFIG_PATH", path)

	runMigration(cfg, 0, cfg.Servers[0], 0, cfg.Servers[0].Databases[0], path)

	// Assert config marked completed.
	var got Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &got)
	m := got.Servers[0].Databases[0].Migrate
	if m == nil || !m.Completed || m.Error != "" {
		t.Fatalf("migration not completed cleanly: %+v", m)
	}

	// Assert target has the rows.
	tgtDBConn := tgtConn[:len(tgtConn)-len("/postgres")] + "/mige2e"
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

	// Assert source database dropped.
	var exists bool
	sdb.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='mige2e')`).Scan(&exists)
	if exists {
		t.Fatal("expected source database to be dropped")
	}
}
```

- [ ] **Step 2: Run it (skips without env)**

Run: `go test -tags integration -run TestMigration_EndToEnd ./...`
Expected: PASS (SKIP) when env vars unset. If a second Postgres is available, set the two env vars and expect a real PASS.

- [ ] **Step 3: Add the docker-compose target and CLAUDE.md docs**

In `docker-compose.yml`, add a second postgres service (mirror the existing `postgres` service, different port + volume):

```yaml
  postgres-target:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
    ports:
      - "5433:5432"
```

(Match the exact image tag / healthcheck style of the existing `postgres` service — read it first and copy its shape.)

In `config-multi.json`, add a `"name": "postgres-target"` server entry with `"root_connection_string": "postgres://postgres:postgres@postgres-target:5432/postgres"` and an empty `"databases": []` so it can be a migration target.

In `CLAUDE.md`, under `## Architecture`, add:

```markdown
### Database migration (PostgreSQL only)

`migrate.go` owns database migration — moving a PostgreSQL database from one
`DatabaseServer` to another, primarily for major-version upgrades.

A `migrate` block on a database entry (`target_server`, optional `confirm_drop`)
is picked up by a dedicated pass at the top of `processConfig`, which runs
*before* normal provisioning so the backup fallback works even when the source
server is unreachable. `runMigration`:

1. Resolves `target_server` (must exist in `config.Servers` and be PostgreSQL).
2. Refuses if the target database already exists; otherwise `provisionPostgreSQL`
   creates the role/database/extensions/permissions on the target.
3. Streams `pg_dump` (source) → `psql` (target). If `pg_dump` cannot reach the
   source, falls back to `findNewestBackup` + `restorePostgreSQL`.
4. Verifies by comparing per-base-table row counts source vs target
   (`verifyMigration`). Backup-fallback path only checks the target is non-empty.
5. On success sets `migrate.completed` + `completed_at`. If `confirm_drop`,
   runs `DROP DATABASE ... WITH (FORCE)` on the source (role left intact).

Failures are recorded in `migrate.error`, never fatal. A completed entry becomes
a tombstone: `processConfig` and `runBackups` skip it. The operator then removes
the entry or relocates it to the target server. The admin UI writes/clears the
`migrate` block via `POST /migrate-database`.

Verification limitation: only base-table row-count parity is checked; sequences,
views, functions, and grants are restored by `pg_dump` but not independently
verified.
```

Also add `go test -tags integration ./...` to the Commands section if a migration/integration line fits.

- [ ] **Step 4: Verify build + full test suite**

Run: `go build ./... && go test ./... && go vet ./...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md docker-compose.yml config-multi.json migrate_integration_test.go
git commit -m "docs: document database migration; add integration test and compose target"
```

---

## Self-Review

**1. Spec coverage:**

| Spec section | Task |
|---|---|
| `MigrateConfig` type + field | Task 1 |
| Migration as its own pass, before provisioning | Task 6 |
| Dry-run logs only | Task 6 (migration pass `server.DryRun` branch) |
| Tombstone skip in provisioning + backups | Task 6 |
| `runMigration` resolve/prepare/dump/verify/drop/persist | Task 5 |
| `resolveTargetServer` errors | Task 3 |
| Target-exists refusal | Task 5 |
| `pg_dump \| psql` streaming | Task 4 |
| Connection-error detection + backup fallback | Task 3 (`isConnectionError`) + Task 5 (wiring) |
| `connStrWithDB` shared helper | Task 2 |
| Verification (`compareTableCounts`, `verifyMigration`, `verifyTargetNonEmpty`) | Task 3 + Task 4 |
| `ponytail:` comment on count queries | Task 4 |
| Config persistence via re-read under lock | Task 5 (`persistMigrate`) |
| `dropPostgreSQLDatabase` WITH (FORCE), role untouched | Task 4 |
| Admin `/migrate-database` handler | Task 7 |
| Admin template control + `PGServerNames` | Task 8 |
| No JSON API endpoint this iteration | (intentionally omitted) |
| Error-handling summary table | Tasks 5, 7 cover each row |
| CLAUDE.md subsection | Task 9 |
| Integration test + compose target | Task 9 |

No gaps.

**2. Placeholder scan:** No "TBD"/"TODO"/"handle edge cases" — every code step has literal code. Integration test env-var setup is explicit.

**3. Type consistency:**
- `runMigration(config *Config, serverIdx int, source DatabaseServer, dbIdx int, db DatabaseConfig, configPath string)` — same signature in Task 5 definition and Task 6 call site.
- `persistMigrate(configPath string, serverIdx, dbIdx int, m MigrateConfig)` — consistent Tasks 5, (referenced) 6.
- `resolveTargetServer(config *Config, targetName, sourceName string) (DatabaseServer, error)` — consistent Tasks 3, 5, 7.
- `connStrWithDB(rootConnStr, database string) (string, error)` — consistent Tasks 2, 4.
- `compareTableCounts(source, target map[string]int64) error` — consistent Tasks 3, 4.
- `MigrateConfig` field names (`TargetServer`, `ConfirmDrop`, `Completed`, `CompletedAt`, `Error`) — consistent across all tasks and the template.
- Template func `dbType` returns `"postgres"` and the template compares `ne (dbType ...) "postgres"` — consistent within Task 8.
