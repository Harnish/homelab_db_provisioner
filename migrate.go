package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// allDatabasesMigrated reports whether every database entry on the server has a
// completed migration (so the server no longer needs provisioning or backups).
func allDatabasesMigrated(server DatabaseServer) bool {
	if len(server.Databases) == 0 {
		return false
	}
	for _, db := range server.Databases {
		if db.Migrate == nil || !db.Migrate.Completed {
			return false
		}
	}
	return true
}

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

// ponytail: O(tables) exact count() queries, full scan each. Fine for homelab DB
// sizes; swap to pg_class.reltuples estimates if this gets slow.
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

var connErrMarkers = []string{
	"could not connect",
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
