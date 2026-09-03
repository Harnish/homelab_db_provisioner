package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
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
