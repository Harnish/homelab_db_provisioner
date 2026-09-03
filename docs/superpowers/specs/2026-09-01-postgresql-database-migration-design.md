# PostgreSQL Database Migration — Design

**Date:** 2026-09-01
**Status:** Approved, ready for implementation plan

## Goal

Move a PostgreSQL database from one `DatabaseServer` in the config to another —
primarily to upgrade across major PostgreSQL versions. Migration is a full
logical dump + restore cutover of a single database, driven by config (picked up
by the watch loop) or triggered from the admin UI (which just writes the config).

Non-goals: MariaDB/MongoDB migration, schema/version migrations (Flyway-style),
zero-downtime/logical-replication cutover, moving multiple databases in one
operation.

## Approach

Streaming `pg_dump | psql` from source server to target server, executed by the
existing `processConfig` pass (one-shot or watch loop). Reuses `provisionPostgreSQL`
to create the role/database/extensions/permissions on the target, and
`findNewestBackup` / `restorePostgreSQL` as the fallback dump source. No new
execution model, no new dependencies, no temp files on the happy path.

The Docker image already bundles `pg_dump` and `psql` (`postgresql18-client`),
which is the supported direction for a major-version upgrade (dump from the old
server with the new client, restore into the new server).

## Config shape

New file `migrate.go` holds `MigrateConfig` and all migration logic.

Add to `DatabaseConfig` (`main.go`):

```go
Migrate *MigrateConfig `json:"migrate,omitempty"`
```

```go
type MigrateConfig struct {
    TargetServer string `json:"target_server"`          // Name of a DatabaseServer already present in config.Servers
    ConfirmDrop  bool   `json:"confirm_drop,omitempty"`  // drop the database on the source server after a verified restore
    Completed    bool   `json:"completed,omitempty"`     // set true by the provisioner on success
    CompletedAt  string `json:"completed_at,omitempty"`  // RFC3339 timestamp, set on success
    Error        string `json:"error,omitempty"`         // last failure message; cleared ("") on success
}
```

## Execution flow

Migration runs as its **own pass** at the top of `processConfig`, after the
backup-summary block and before the "Process each server" provisioning loop.
This is deliberate: the fallback ("source unreachable → restore newest backup")
must work even when the source root connection is dead, but the main
provisioning loop `continue`s past a server whose root connection fails — so the
migration hook cannot live inside it.

Migration pass — for each `config.Servers[si]` whose `RootConnectionString` is
PostgreSQL, for each `Databases[di]`:

1. `Migrate == nil || Migrate.Completed` → skip (nothing to do this pass).

2. `server.DryRun` → log `[DRY RUN] would migrate <server>/<db> to <target>
   (confirm_drop=<bool>)` and skip. No connections opened, no config write.

3. Otherwise → `runMigration(config, si, server, di, dbConfig, getConfigPath())`.

Then, in the existing provisioning loop, each database entry is checked:

- `Migrate != nil && Migrate.Completed` → tombstone. Log and `continue` — skip
  provisioning **and** skip backups for this entry. (The live database now lives
  on the target server; the operator removes or relocates the config entry
  manually.)
- `Migrate != nil && !Migrate.Completed` (pending or failed) → fall through to
  **normal provisioning** as usual. Keeping the source database healthy while a
  migration is pending or being debugged is the safe default; the next pass
  retries the migration.

Backups: `runBackups` / the scheduler must apply the same tombstone skip —
`Migrate != nil && Migrate.Completed` → do not back up that entry.

### `runMigration(config *Config, serverIdx int, source DatabaseServer, dbIdx int, db DatabaseConfig, configPath string)`

Signature returns nothing. All failure paths: set `Error`, leave
`Completed=false`, persist via the "Config persistence" write, log, and return
(never fatal — a failed migration must not stop the rest of `processConfig`).

1. **Resolve target.** Find `config.Servers[i].Name == db.Migrate.TargetServer`.
   Not found → error `"target server %q not found"`. `detectDBType` of its
   `RootConnectionString` is not `PostgreSQL` → error `"target server %q is not
   PostgreSQL"`. Target server name == source server name → error
   `"target server must differ from source"`.

2. **Prepare target.** Connect to the target root connection string. Check
   whether the target database already exists (`checkPostgreSQLDatabaseExists`).
   - Exists → error `"target database %q already exists on %q, refusing to
     overwrite"`. (Operator resolves by dropping it or clearing the migrate
     block after confirming.)
   - Does not exist → call `provisionPostgreSQL(targetDB, targetConnStr, *db,
     false)` to create the role (with password), database (owner = `db.User`),
     extensions, and permissions.

3. **Dump + restore.**
   - Primary: stream `pg_dump --no-password <sourceConnStr with path=/db>`
     stdout directly into `psql --no-password <targetConnStr with path=/db>`
     stdin, using an `io.Pipe` or `cmd.StdoutPipe`. `psql` runs with default
     leniency (no `ON_ERROR_STOP`); its stderr is captured and logged but a
     non-zero `psql` exit is **not** by itself a migration failure — step 4 is
     the correctness gate. A non-zero `pg_dump` exit **is** a failure unless it
     is a connection error (see fallback).
   - Fallback (source unreachable): if `pg_dump` exits non-zero **and** its
     stderr looks like a connection error (`isConnectionError`), call
     `findNewestBackup(config, configPath, source.Name, db.Database,
     PostgreSQL)`. If it returns a file, `restorePostgreSQL(targetConnStr,
     db.Database, backupFile)` and set `fromBackup = true`. If it returns `""` →
     error `"source unreachable and no backup found"`.
   - Connection-string rewriting (set URL path to `/<database>`) uses the shared
     `connStrWithDB` helper (extracted from `backupPostgreSQL` /
     `restorePostgreSQL` as part of this work).

4. **Verify.**
   - Normal path (`!fromBackup`): `verifyMigration(sourceConnStr, targetConnStr,
     db.Database)` — see "Verification". Any error → migration failure (target
     database is left in place for inspection; no drop).
   - Backup fallback (`fromBackup`): the source is unreachable, so a
     source-vs-target comparison is impossible. Run `verifyTargetNonEmpty(
     targetConnStr, db.Database)` (target has ≥1 base table) and log a warning
     that full verification was skipped. Error → migration failure.

5. **Success.** `Completed = true`,
   `CompletedAt = time.Now().UTC().Format(time.RFC3339)`, `Error = ""`.
   If `ConfirmDrop`: `dropPostgreSQLDatabase(source.RootConnectionString,
   db.Database)` runs `DROP DATABASE <quoted db> WITH (FORCE)` on the source.
   The source role is left untouched (it may own other databases / be shared).
   A failed drop sets `Error` to the drop error but keeps `Completed = true`
   (the migration itself succeeded). `ConfirmDrop` with `fromBackup` will
   almost certainly fail the drop (source is down) — that is fine and expected.

6. Persist the final `MigrateConfig` (see "Config persistence").

## Verification

`verifyMigration(sourceConnStr, targetConnStr, database string) error`:

1. Connect to each side at the specific database. Query both:

   ```sql
   SELECT table_schema, table_name
   FROM information_schema.tables
   WHERE table_type = 'BASE TABLE'
     AND table_schema NOT IN ('pg_catalog', 'information_schema')
   ORDER BY 1, 2
   ```

   Table-set difference → error listing tables present on only one side.

2. For each table, `SELECT count(*) FROM "schema"."table"` on both sides
   (identifiers quoted with the existing `quoteIdentifier`). Count mismatch →
   error naming the table and both counts. Stop at the first mismatch.

3. All match → `nil`.

The set-diff and count-diff logic is factored into a pure helper
`compareTableCounts(source, target map[string]int64) error` (key =
`"schema.table"`) so it is unit-testable without a database; `verifyMigration`
only does the querying.

`verifyTargetNonEmpty(targetConnStr, database string) error` — the fallback
check: runs the same base-table query against the target only, errors if the
count is zero.

Known limitation (documented in CLAUDE.md): sequences, views, functions, and
grants are restored by `pg_dump` but not independently verified; row-count
parity per base table is the correctness signal.

`// ponytail: O(tables) exact count() queries, full scan each. Fine for homelab
DB sizes; swap to pg_class.reltuples estimates if this gets slow.`

## Config persistence

`processConfig` runs **without** holding `configMu` (it operates on an in-memory
copy loaded under `RLock` before the pass). So `runMigration` persists its result
directly, using the same pattern every admin handler uses:

```
configMu.Lock()
re-read configPath from disk, json.Unmarshal into a fresh Config
cfg.Servers[serverIdx].Databases[dbIdx].Migrate = &finalMigrateConfig
json.MarshalIndent + os.WriteFile(configPath, out, 0600)
configMu.Unlock()
```

Re-reading from disk (rather than writing back the whole in-memory `config`)
avoids clobbering an admin edit made during a long-running pass. `runMigration`
therefore takes `serverIdx` and `dbIdx` alongside the server and db values. The
`DROP DATABASE` on the source and all dump/restore/verify work happen before this
write.

## Admin UI

Route: `mux.HandleFunc("/migrate-database", handleMigrateDatabase(configPath))`
in `newAdminHandler` (`admin.go`).

`handleMigrateDatabase` mirrors `handleAddDatabase`:

- POST only. Form fields: `server_index`, `db_index`, `target_server`,
  `confirm_drop` (`"on"`).
- `configMu.Lock()`, read + unmarshal config.
- Validate `server_index` / `db_index` in range.
- If `target_server` is empty → clear: set `Migrate = nil` (used to retry a
  failed migration). Otherwise validate the named server exists, is PostgreSQL,
  and differs from the source; set
  `Migrate = &MigrateConfig{TargetServer: target_server, ConfirmDrop: confirm_drop == "on"}`.
- `json.MarshalIndent` + `os.WriteFile` `0600`, redirect to `/?msg=...`.

Template (inline template in `admin.go`): each database row gains a migration
control.

- `Migrate == nil` → collapsed "Migrate" form: `<select name="target_server">`
  populated with the names of all **other** PostgreSQL servers in the config, a
  `confirm_drop` checkbox with a warning label, and a submit button. Hidden
  `server_index` / `db_index`.
- `Migrate != nil && !Completed && Error == ""` → badge `migrating…` (watch loop
  will act on next poll).
- `Migrate != nil && Completed` → badge `migrated to <TargetServer> at
  <CompletedAt>` + a "clear" button (POST with empty `target_server`).
- `Migrate != nil && Error != ""` → badge `migration error: <Error>` + a "retry"
  button (clears `Migrate`) — operator re-adds the migrate block after fixing
  the cause.

`adminTemplateData` gains `PGServerNames []string` — the names of all PostgreSQL
servers, computed in `handleIndex`. The template filters out the current server
per-row when rendering the `<select>` (compare against `$server.Name`).

No JSON API endpoint in this iteration (the existing `/api` surface can add
`migrate` to the database PATCH body later if needed — out of scope here).

## Error handling summary

| Situation | Result |
|---|---|
| Target server missing / not PG / == source | `Error` set, no changes, config persisted |
| Target database already exists | `Error` set, no dump attempted |
| `pg_dump` connection failure + backup found | fallback restore from newest backup |
| `pg_dump` connection failure + no backup | `Error` set |
| `pg_dump` non-connection failure | `Error` set |
| `psql` non-zero exit | logged; not a failure by itself |
| Verification table-set / row-count mismatch | `Error` set, target DB left intact, source not dropped |
| Verification passes | `Completed=true`, `CompletedAt` set, `Error=""` |
| `ConfirmDrop` + verified | `DROP DATABASE` on source |
| `DROP DATABASE` on source fails | `Error` set to drop error, `Completed` stays `true` |
| `server.DryRun` | log only, no connections, no config write |

## Testing

Follow the repo's existing table-driven `*_test.go` style (`main_test.go`,
`admin_test.go`, `backup_test.go`). No new frameworks.

Unit / pure logic:

- `resolveTargetServer` — found / not found / not PostgreSQL / same as source.
- Migration-status collection: `processConfig` on a config with
  `Migrate.Completed == true` skips provisioning + backup for that entry
  (assert `provisionPostgreSQL` not reached — via a config whose source conn
  string would fail, expecting no error).
- Admin `handleMigrateDatabase` — happy path writes the `migrate` block;
  empty `target_server` clears it; invalid target rejected; out-of-range
  indices rejected. Assert the written file content (same approach as
  `handleAddDatabase` tests).
- Template renders the "Migrate" form for `Migrate == nil` and the correct
  badge for each `Migrate` state (string checks on rendered HTML).

Integration (guarded by the existing Postgres-available pattern in the repo, if
any; otherwise a `//go:build integration` tag):

- End-to-end migrate between two local Postgres instances (docker-compose adds a
  second postgres service or reuses `config-multi.json` targets): seed source,
  set `migrate` block, run `processConfig`, assert target has the rows,
  `Completed == true`, and — with `confirm_drop` — source database is gone.
- `verifyMigration` row-count mismatch path (delete a row on the target before
  verify, expect error).

## CLAUDE.md update

Add a "Database migration" subsection under Architecture describing: the
`migrate` block, that `migrate.go` owns it, the dump→restore→verify→optional-drop
flow, the backup fallback, the tombstone end-state (completed entries are skipped;
operator relocates them), and the verification limitation (row-count parity per
base table only).

## Files touched

- `main.go` — `MigrateConfig` type, `DatabaseConfig.Migrate` field, the
  migration pass at the top of `processConfig`, and the tombstone skip in the
  provisioning loop.
- `backup.go` — extract `connStrWithDB(rootConnStr, database string) (string,
  error)`; refactor `backupPostgreSQL` and `restorePostgreSQL` to use it; add
  the tombstone skip to `runBackups`.
- `migrate.go` — **new**: `MigrateConfig` logic — `runMigration`,
  `resolveTargetServer`, `migratePostgreSQLData` (streaming `pg_dump | psql`),
  `pgDumpError` + `isConnectionError`, `verifyMigration`, `compareTableCounts`,
  `verifyTargetNonEmpty`, `dropPostgreSQLDatabase`, `persistMigrate`.
- `admin.go` — `handleMigrateDatabase`, route registration, template migration
  control, `adminTemplateData.PGServerNames`, `handleIndex` populates it.
- `migrate_test.go` — **new**: unit tests (see Testing).
- `admin_test.go` — `handleMigrateDatabase` tests, template-render tests.
- `main_test.go` — tombstone-skip test for `processConfig`.
- `CLAUDE.md` — Database migration subsection.
- `docker-compose.yml`, `config-multi.json` — second postgres service +
  integration test config (integration test itself is `//go:build integration`
  tagged and can land in the same task or be deferred — flag in the plan).
