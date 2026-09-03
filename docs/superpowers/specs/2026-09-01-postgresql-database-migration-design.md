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

In `processConfig`, PostgreSQL branch, evaluated per database entry **before**
the normal provisioning call for that entry:

1. `Migrate != nil && Migrate.Completed` → this entry is a tombstone. Skip
   provisioning and skip backups for it. `continue`. (The live database now
   lives on the target server; the operator removes or relocates the config
   entry manually.)

2. `Migrate != nil && !Migrate.Completed && !server.DryRun` → call
   `runMigration(config, sourceServer, &dbConfig, configPath)`, then `continue`
   (do not also run normal provisioning on the source this pass).

3. `Migrate != nil && server.DryRun` → log `[DRY RUN] would migrate <db> from
   <source> to <target> (confirm_drop=<bool>)` and `continue`. No connections
   opened, no config write.

### `runMigration(config *Config, source DatabaseServer, db *DatabaseConfig, configPath string)`

All failure paths: set `db.Migrate.Error`, leave `Completed=false`, log, and
return the mutated `MigrateConfig` to `processConfig` as a pending status update
(the actual file write is deferred — see "Config persistence"). Never fatal — a
failed migration must not stop the rest of `processConfig`.

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
   - Fallback (source unreachable): if `pg_dump` fails to connect to the source,
     call `findNewestBackup(config, configPath, source.Name, db.Database,
     PostgreSQL)`. If it returns a file, `restorePostgreSQL(targetConnStr,
     db.Database, backupFile)`. If it returns `""` → error `"source unreachable
     and no backup found"`.
   - Connection-string rewriting (set URL path to `/<database>`) mirrors
     `backupPostgreSQL` / `restorePostgreSQL`.

4. **Verify.** `verifyMigration(sourceConnStr, targetConnStr, db.Database)` — see
   "Verification". Any error → migration failure (target database is left in
   place for inspection; no drop).

5. **Success.** `db.Migrate.Completed = true`,
   `db.Migrate.CompletedAt = time.Now().UTC().Format(time.RFC3339)`,
   `db.Migrate.Error = ""`. If `db.Migrate.ConfirmDrop`: run
   `DROP DATABASE <quoted db>` on the **source** root connection. The source
   role is left untouched (it may own other databases / be shared). A failed
   drop sets `Error` to the drop error but keeps `Completed = true` (the
   migration itself succeeded).

6. Return the final `MigrateConfig` to `processConfig` as a pending status
   update; the file write happens after the pass (see "Config persistence").

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

Known limitation (documented in CLAUDE.md): sequences, views, functions, and
grants are restored by `pg_dump` but not independently verified; row-count
parity per base table is the correctness signal.

`// ponytail: O(tables) exact count() queries, full scan each. Fine for homelab
DB sizes; swap to pg_class.reltuples estimates if this gets slow.`

## Config persistence

`runMigration` and the admin handler both mutate the on-disk config. Follow the
existing admin pattern: hold `configMu.Lock()`, re-read the file from disk,
unmarshal, apply the mutation by server index + database index, `json.MarshalIndent`,
`os.WriteFile(configPath, out, 0600)`.

`runMigration` is called from `processConfig`, which already holds
`configMu.RLock()` for the duration of the pass. To avoid a lock upgrade, the
migration result is written via a small helper that is invoked **after** the
provisioning loop releases the read lock — i.e. `processConfig` collects pending
migration-status updates `(serverIdx, dbIdx, MigrateConfig)` during the pass and
applies them under `configMu.Lock()` at the end, re-reading the file first. The
`DROP DATABASE` on the source and all dump/restore/verify work happen during the
pass; only the file write is deferred.

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

`adminTemplateData` needs the list of PostgreSQL server names available for the
`<select>` (compute in `handleIndex`, or derive in the template from `Servers`
with a helper). Deriving in `handleIndex` and adding a field to
`adminTemplateData` is preferred for testability.

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

- `main.go` — `MigrateConfig` type, `DatabaseConfig.Migrate` field, migration
  hook + deferred status-write in `processConfig`.
- `migrate.go` — **new**: `runMigration`, `resolveTargetServer`,
  `verifyMigration`, dump/restore streaming helper, source `DROP DATABASE`.
- `admin.go` — `handleMigrateDatabase`, route registration, template migration
  control, `adminTemplateData` PG-server-names field, `handleIndex` populates it.
- `migrate_test.go`, `admin_test.go` — tests per above.
- `CLAUDE.md` — Database migration subsection.
- `docker-compose.yml` / `config-multi.json` — optionally a second postgres
  target for integration testing (can be deferred).
