# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run all tests
go test ./...

# Run a single test
go test -run TestUpdatePassword_Success ./...

# Build the binary
go build -o provisioner .

# Run locally (one-shot)
CONFIG_PATH=./config.json go run .

# Run locally (watch mode)
WATCH_MODE=true CONFIG_PATH=./config.json go run .

# Run with admin UI
ADMIN_SITE=true ADMIN_USER=admin ADMIN_PASSWORD=secret WATCH_MODE=true CONFIG_PATH=./config.json go run .

# Docker Compose (starts postgres + mariadb + provisioner using config-multi.json)
docker-compose up --build
```

## Architecture

The entire application lives in four Go files in the package root (`package main`):

- **`main.go`** — config types, DB provisioning logic, watch loop
- **`admin.go`** — optional HTTP admin UI (view/add databases, change passwords)
- **`backup.go`** — backup scheduler, pg_dump/mysqldump execution, retention pruning
- **`mongo.go`** — MongoDB provisioning, mongodump/mongorestore, backup scheduling

### Data flow

1. `main()` reads `CONFIG_PATH` and optionally starts the admin server in a goroutine.
2. In one-shot mode, `loadConfig()` → `processConfig()` runs once and exits.
3. In watch mode, a polling loop (`runWatchMode`) checks the config file's mtime every 10 seconds and re-runs `processConfig()` on change.
4. `processConfig()` iterates over `Config.Servers`, detects DB type from the connection string prefix (`postgres://` vs `mariadb://`/`mysql://`), converts MariaDB URLs to the go-sql-driver DSN format, then calls `provisionPostgreSQL` or `provisionMariaDB` per database entry.

### Concurrency

`configMu` (a `sync.RWMutex` in `main.go`) guards all config file reads/writes. The admin HTTP handlers hold the write lock when modifying the config file; the watch loop and provisioner hold the read lock. This prevents races between the admin UI and the watch loop.

### Admin UI

`admin.go` exposes three HTTP endpoints behind HTTP Basic Auth:
- `GET /` — renders the inline Go template with the current config state
- `POST /update-password` — updates a password by `server_index`/`db_index`, writes config to disk
- `POST /add-database` — appends a new `DatabaseConfig` to a server, writes config to disk

Config writes use `json.MarshalIndent` and `os.WriteFile` with `0600` permissions. The admin UI is most useful with `WATCH_MODE=true` since the provisioner auto-picks up the written changes.

### DB type detection

`detectDBType` inspects the connection string prefix. MariaDB/MySQL URLs are reformatted from `mariadb://user:pass@host:port/db` to the `go-sql-driver/mysql` DSN `user:pass@tcp(host:port)/db` inline in `processConfig`.

### Backup scheduler

`startBackupScheduler` (called as a goroutine from `main`) sleeps until the next midnight, then calls `runBackups`. It reads the live config (under `configMu.RLock`) on each tick so config changes are picked up without a restart.

`provisionPostgreSQL` and `provisionMariaDB` now return `(bool, error)` where the bool signals that the database was **newly created** (did not previously exist). `processConfig` uses this to gate the optional restore: if `created && !dryRun && backup.restore_on_create`, it calls `restoreDatabase`, which calls `findNewestBackup` (globs `backups/{server-slug}/{database}/{database}_*.sql.gz`, sorts alphabetically, returns the last entry) and pipes the decompressed file into `psql` or `mysql`. `MariaDB` added `checkMariaDBDatabaseExists` to support the created-flag logic.

`runBackups` iterates all servers/databases with `backup.enabled: true`. Daily backups run every midnight; weekly backups run only when `time.Now().Weekday() == time.Sunday`. It calls `backupPostgreSQL` (which rewrites the connection string path to the target DB and shells out to `pg_dump`) or `backupMariaDB` (shells out to `mysqldump` with `MYSQL_PWD` set in the subprocess env). Output is gzip-compressed directly from the command's stdout into `{config_dir}/backups/{server-slug}/{database}/{database}_{YYYY-MM-DD}.sql.gz`. After a successful backup, `pruneBackups` sorts the glob matches alphabetically (date-prefixed names sort chronologically) and deletes the oldest files beyond `keep_count`.

The Dockerfile final stage uses `alpine:3.24` (instead of distroless) to provide `pg_dump` (`postgresql18-client`) and `mysqldump` (`mariadb-client`). MongoDB tools (`mongodump`/`mongorestore`) are not bundled and must be installed separately.

When `Config.S3` is set (top-level `s3` block: `bucket`, `region`, optional `endpoint` for MinIO/S3-compatible targets, optional `prefix`), every successful local backup is also uploaded to `s3://{bucket}/{prefix}/{server-slug}/{database}/{filename}` and pruned to the same `keep_count` as the local copy. S3 credentials come from the environment (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/etc., the AWS SDK default chain) — never from `config.json`. If `restore_on_create` triggers a restore and no local backup file exists (e.g. a fresh container with an empty `backups/` volume), `findNewestBackup` falls back to downloading the newest object from S3. All S3 operations are logged and non-fatal; local backup/restore is unaffected if S3 is unreachable or unconfigured.

### Dry-run mode

Each `DatabaseServer` has an optional `dry_run: true` field. When set, all SQL statements are logged with `[DRY RUN]` prefix but not executed.

## Key environment variables

| Variable | Default | Purpose |
|---|---|---|
| `CONFIG_PATH` | `/config/config.json` | Path to config file |
| `WATCH_MODE` | `false` | Poll config file for changes |
| `ADMIN_SITE` | — | `true` to enable admin HTTP server |
| `ADMIN_USER` / `ADMIN_PASSWORD` | — | Basic Auth credentials (required when `ADMIN_SITE=true`) |
| `ADMIN_PORT` | `8080` | Admin server port |
