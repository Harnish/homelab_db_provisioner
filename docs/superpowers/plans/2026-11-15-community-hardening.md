# Community hardening plan

Written 2026-11-15 from a full read of the non-test Go sources, CI, Dockerfile, manifests and README at commit `41b04b9`. The project is now public, so strangers will run it against their own databases with their own names, passwords and config shapes. This plan fixes what breaks or leaks under that use.

`go vet ./...` and `go test ./...` pass at this commit. Nothing below was reproduced against a live database; every finding comes from reading the code. Each task says to write the failing test first, which doubles as confirmation.

## Status (2026-11-16)

All seven workstreams are implemented on branch `community-hardening`. The rpm dependencies in `nfpm.yaml` were checked on Fedora 45: `postgresql` provides `pg_dump`/`psql` and `mariadb` provides `mysqldump`/`mysql`. Unit tests pass; the integration suite passes against PostgreSQL 15 and 18, MariaDB 11 and MongoDB 7, including inside the built image.

Two bugs turned up during testing that this review missed, both fixed: MongoDB provisioning failed on the first run too (the database-list type assertion never matched), and MongoDB backups always failed with the documented `/admin` connection string (`mongodump` rejects a `--db` that differs from the URI database).

Departures from the tasks below:
- E3: restores still run to the end and log a warning when statements fail. `ON_ERROR_STOP=1` was not added, because one harmless error would roll back the whole restore.
- C2: beyond the task, one-shot mode now exits 1 when a server is unreachable (owner approved), so a Job or systemd unit retries or reports it.
- A4: MariaDB connection strings are not URL-decoded, to avoid breaking passwords that contain `%`.
- D4: no probes were added to the example manifests, because they do not enable the admin server.
- G1: the test workflow does not gate the image and package workflows; use branch protection.

## How to use this plan

- One Claude per workstream. Workstreams are grouped by file so they do not collide. Where two streams touch `main.go`, the functions are named.
- Start with workstream G1 (CI test job). Everything else lands on top of it.
- Read `CLAUDE.md` first. Run `go test ./...` before and after. Use `graft callers <symbol>` before changing any shared function.
- Conventional commits (`fix:`, `feat:`, `chore:`). Do not commit without the owner's confirmation.
- Smallest change that fixes the root cause. No new dependencies, no new abstractions, no refactors beyond the task.
- Do not break the JSON API shape. Existing scripts call `GET /api/servers`, pick a server by `name`, read `index` and `databases[].database`, then `POST /api/servers/<index>/databases` and read `generated_password`.
- Never print or log a password, in code, tests output or commit messages.

## Priority order

| # | Workstream | Why it matters | Files |
|---|---|---|---|
| G1 | CI runs tests | No workflow runs `go test` today | `.github/workflows/` |
| A | SQL safety and password leaks | Injection through names, passwords in logs | `main.go` provisioning functions |
| B | MongoDB correctness | Second run likely fails for every Mongo database | `mongo.go`, `Dockerfile` |
| C | Migration and watch loop | Failed migration loops every 10 s and hides its cause | `migrate.go`, `main.go` watch loop |
| D | Config writes and API | Non-atomic writes of the file holding every password, root password returned by the API | `api.go`, `admin.go`, `migrate.go` |
| E | Backups | Credentials in process arguments, world-readable dumps, silent restore errors | `backup.go`, `s3.go` |
| G2 | Release and repo hygiene | No LICENSE file, broken example manifest, version flag does nothing | repo root, docs |

A and D share an input validator: A defines it, D calls it. Do A1 before D3.

---

## G1. CI test job

No workflow runs tests. `docker-build.yml` and `packages.yml` only build.

- Add a job (new `test.yml` or a job in an existing workflow) on push and pull request: `go vet ./...`, `go test -race ./...`.
- Add an integration job with service containers for `postgres:15`, `postgres:18` and `mariadb`, running `go test -tags integration ./...` with `MIGRATE_SOURCE_CONN` and `MIGRATE_TARGET_CONN` set. Workstreams A and B add integration tests that use these.
- Make the image and package jobs depend on the test job.

Done when: a pull request with a failing test is blocked.

---

## A. SQL safety and password leaks (`main.go`)

One Claude, tasks in order.

### A1. Identifier quoting and input validation

- `quoteIdentifier` (`main.go:758`) wraps in double quotes without escaping embedded `"`. A database or user name containing `"` breaks out of the identifier. It is used for `CREATE USER`, `CREATE DATABASE`, `ALTER`, `GRANT`, `CREATE EXTENSION`, `DROP DATABASE ... WITH (FORCE)` (`migrate.go:240`) and the count queries (`migrate.go:285`).
- MariaDB statements interpolate `config.Database` inside backticks with no escaping (`main.go:631`, `685`, `691`).
- `escapeString` (`main.go:762`) doubles single quotes only. MariaDB in its default SQL mode also treats backslash as an escape, so a user name or password ending in `\` breaks the literal.
- `config.Permissions` is joined straight into `GRANT` (`main.go:562`, `684`).

Fix:
- Replace `quoteIdentifier` with `pq.QuoteIdentifier` (already a dependency). For string literals on PostgreSQL use `pq.QuoteLiteral`.
- For MariaDB, escape backticks in identifiers (double them) and escape backslashes as well as quotes in literals.
- Add one validator, `validateDatabaseConfig(DatabaseConfig) error`: database and user names non-empty, at most 63 bytes, matching `^[A-Za-z_][A-Za-z0-9_$-]*$`; permissions and extensions matching `^[A-Za-z_ ]+$` and `^[A-Za-z0-9_-]+$`. Call it in `processConfig` before provisioning each entry (log and skip on failure, as other per-database failures do).

Tests: table test for the validator, and one for quoting with `"`, backtick, `'` and `\` in each position.

### A2. Passwords in dry-run logs

Dry-run logs the full statement including the password: `main.go:487`, `502`, `656`, `671`. Dry run is the first thing a careful new user tries, and logs go to `kubectl logs` and journald.

Fix: build a second copy of the statement with the password replaced by `'***'` for logging. Test by capturing log output and asserting the password is absent.

### A3. PostgreSQL `permissions` cannot work as documented

`provisionPostgreSQL` emits `GRANT <permissions> ON DATABASE` (`main.go:563`). PostgreSQL only accepts `CREATE`, `CONNECT`, `TEMPORARY` at database level, so the shipped examples (`config.json` uses `SELECT, INSERT, UPDATE`; `config-multi.json` uses `SELECT, INSERT`) should fail with `invalid privilege type`. The user is also made database owner, so a restriction would be meaningless anyway.

Recommended fix (owner to confirm, default to this): on PostgreSQL accept only database-level privileges and reject others in the validator with a clear message; remove `permissions` from the PostgreSQL entries in `config.json`, `config-multi.json`, the manifests and the README, and document that the user owns the database. Keep MariaDB behavior unchanged. Add an integration test that provisions the example config against the service container.

### A4. One MariaDB connection-string parser

`processConfig` converts the URL by splitting on `/` and `@` (`main.go:288-303`), which breaks on a password containing either. `parseMariaDBConn` (`backup.go:180`) already parses with `LastIndex`. Use `parseMariaDBConn` plus `mysql.Config.FormatDSN` (driver already imported) in `processConfig`, URL-decode user and password, and delete the inline splitter. Test with `p@ss/word` and a percent-encoded password.

---

## B. MongoDB (`mongo.go`, `Dockerfile`)

### B1. Second provisioning run

`provisionMongoDB` inserts `{_id: "provisioned"}` into `<db>_data` on every run (`mongo.go:87`) and only tolerates errors containing `already exists`. A repeat insert returns a duplicate-key error (`E11000`), so the function should return `failed to create collection` on every run after the first. Fix: use `mongo.IsDuplicateKeyError(err)`, or upsert. Confirm with an integration test that provisions twice (add a `mongo` service container to the G1 integration job).

### B2. Drop and recreate user

Each run drops the user and recreates it (`mongo.go:58-73`). Between the two commands the application has no account, and this happens on every config change for every Mongo database. Fix: `usersInfo` to check, then `updateUser` (password and roles) or `createUser`. Update the README "Behavior" section, which documents the drop.

### B3. Backups run N times with N Mongo servers

`runBackups` calls `mongoDBBackupSchedule` once per Mongo server (`backup.go:56`), and that function loops over all Mongo servers itself (`mongo.go:168`). Fix: back up only the server passed in. Fold the duplicate loop body into `runBackups` if that is shorter; `pruneMongoDBBackups` duplicates `pruneBackups` apart from the file suffix.

### B4. Small items

- Delete `parseMongoDBConnStr` (`mongo.go:141`), unused.
- `mongodump`/`mongorestore` are not in the image. Check whether Alpine 3.24 ships `mongodb-tools`; if so add it to the Dockerfile, if not make the backup log line say plainly that the tools are missing rather than a generic exec error.
- `mongodump --uri` puts the root password in the process arguments. See E1.

---

## C. Migration and watch loop (`migrate.go`, `main.go:128-167`, `main.go:242-257`)

### C1. Failed migration hot loop and lost error

On failure `runMigration` writes `migrate.error` to the config file (`migrate.go:82-87`). The write changes the file's mtime, the watch loop sees a change 10 s later, re-runs `processConfig`, and the migration pass retries because it only skips `Completed` entries (`main.go:247`). The retry then fails at `target database ... already exists, refusing to overwrite` (`migrate.go:118`), because the first attempt created the target before failing. Result: the real error is replaced by "already exists", the config is rewritten and every server is re-provisioned every 10 s, forever, and the migration can never succeed without a manual `DROP DATABASE` on the target.

Fix, in this order:
1. Do not retry an entry whose `migrate.error` is set. The operator clears it by resubmitting the migration (the admin handler already rewrites the block; confirm it clears `error`). Change the UI text "retrying" (`admin.go:252`) to match.
2. When a migration fails after this run created the target database, drop the target database it just created (only when this run created it; `provisionPostgreSQL` returns that flag) so a resubmit starts clean.
3. `persistMigrate` should skip the write when the block is unchanged.

Tests: unit test that a config with `migrate.error` set is not passed to `runMigration`; extend `migrate_integration_test.go` with a forced verification failure and assert the target database is gone and the original error is kept.

### C2. Watch mode never retries an unreachable server

`runWatchMode` records the mtime before processing (`main.go:147`). If a server is down at startup, `processConfig` logs and skips it, and nothing retries until the file changes. In Kubernetes the provisioner often starts before the database.

Fix: have `processConfig` report whether any server or database was skipped on error, and in that case re-run after a longer interval (60 s) without waiting for an mtime change. Do not retry on validation errors.

### C3. Index-based persistence

`persistMigrate` writes by `serverIdx`/`dbIdx` captured before a long dump. If the admin UI deletes an entry meanwhile, the result lands on the wrong database. Fix: re-find the entry by server name and database name under the lock.

---

## D. Config writes and API (`api.go`, `admin.go`, `migrate.go:47-78`)

### D1. One atomic config writer

Nine places call `os.WriteFile(configPath, ...)` directly (`api.go:37`, `admin.go:887`, `970`, `1083`, `1168`, `1350`, `1448`, `1508`, `migrate.go:75`). `os.WriteFile` truncates first, so a crash or full disk mid-write leaves an empty or partial file that holds every password.

Fix: make `saveConfigFile` write to a temp file in the same directory, `fsync`, then rename; use it at all nine sites. A single-file bind mount (as in `docker-compose.yaml:60`) cannot be renamed over: on `EBUSY`/`EXDEV` fall back to the current in-place write and log once. Test both paths.

### D2. Root password in API responses

`serverResponse` returns `root_connection_string` verbatim (`api.go:65`), including the root password, on every `GET /api/servers`. Fix: redact the password in responses (`url.Parse`, `u.User = url.UserPassword(user, "REDACTED")`; handle the MariaDB and Mongo URL forms). Keep the field and everything else unchanged. Check the admin HTML does not render the raw string either (`admin.go:215-219` only passes it to `dbType`; confirm). Note the change in the README API section.

### D3. Validate at the boundary

API and admin handlers accept any database, user, permission, extension and server name. After A1, call `validateDatabaseConfig` in `handleAPICreateDatabase`, `handleAPIUpdateDatabase` and `handleAddDatabase`, returning 400. Also reject: an empty or duplicate server name (migration and backup paths look servers up by name, and `slugify` maps different names to one backup directory), and a duplicate database name on the same server.

### D4. Unauthenticated health endpoint

Add `GET /healthz` returning 200 outside `basicAuth`, for Kubernetes probes. Add probes to the example Deployment manifests. Skip a Dockerfile `HEALTHCHECK`: the image also runs one-shot with no HTTP server.

---

## E. Backups (`backup.go`, `s3.go`)

### E1. Credentials in process arguments

`pg_dump` and `psql` receive the full connection URL with password as an argument (`backup.go:133`, `301`; `migrate.go:205-206`), visible in `ps` to any local user. This matters for the deb/rpm install on a shared host. Fix: strip the password from the URL and pass it as `PGPASSWORD` in `cmd.Env`, as the MariaDB path already does with `MYSQL_PWD`. For Mongo, write the URI to a 0600 temp config file and use `mongodump --config`.

### E2. Dump file permissions

`os.Create` makes dumps 0644 (`backup.go:127`, `156`; `s3.go:153`). Use `os.OpenFile(..., 0600)`.

### E3. Restore reports success on SQL errors

`restorePostgreSQL` runs `psql` without `ON_ERROR_STOP` (`backup.go:301`), so `restore_on_create` logs "complete" after a partly failed restore. Fix: pass `--set ON_ERROR_STOP=1` and `--single-transaction` there. Leave `migratePostgreSQLData` as is; it relies on row-count verification by design.

### E4. Missed midnight

The scheduler only fires at local midnight (`backup.go:20-36`). A restart or outage across midnight skips the day silently. Fix: at scheduler start, and after each tick, run any enabled backup whose newest local file is older than its schedule allows (`backupAge` in `admin.go:611` already computes this; reuse it). Skip the scheduler entirely in one-shot mode (`main.go:103`).

### E5. Partial S3 download

`downloadNewestFromS3` writes straight to the final path (`s3.go:152-161`). A failed copy leaves a truncated file that the next `findNewestBackup` treats as the newest local backup. Fix: download to a temp name, rename on success.

Known ceiling, leave as is: `ListObjectsV2` is not paginated (1000 keys per database prefix).

---

## G2. Release and repo hygiene

- **LICENSE**: README and `nfpm.yaml` say MIT but there is no `LICENSE` file. Add the MIT text (owner confirms the copyright line).
- **Version flag**: `packages.yml` builds with `-X main.version=...` but no `version` variable exists, so the linker ignores it. Add `var version = "dev"`, log it at startup, pass the same flag in the Dockerfile build via a build arg.
- **`configmap.yaml`**: uses the old top-level `root_connection_string`/`databases` shape, which `loadConfig` rejects ("at least one server configuration is required"). Rewrite to the `servers` shape or delete the file if the other manifests cover it.
- **Manifests**: `image: homelab-db-provisioner:latest` in the three Kubernetes manifests should be `ghcr.io/harnish/homelab_db_provisioner:latest` (lowercase). The README never mentions the published image; add a pull line.
- **`.dockerignore`**: missing. Add one (`.git`, `docs`, `graft`, binaries, `*.md`). There are two ~86 MB binaries in the working tree.
- **`.gitignore`**: add `/homelab_db_provisioner` (the underscore-named binary is untracked and not ignored).
- **`docker-compose.yaml`**: drop the obsolete `version:` key.
- **README**: fix the line that says only PostgreSQL and MariaDB are supported; replace the "Security Considerations" list with what the tool actually does and does not protect (plain-text passwords in `config.json`, Basic Auth over HTTP unless fronted by TLS, root credentials required); add a short "Reporting security issues" note.
- **`TODO.md`**: "Package Dependencies" is unchecked, but `nfpm.yaml` already declares client-tool dependencies. Verify the rpm names (`postgresql`, `mariadb`) on Fedora, then tick or fix.
- **CONTRIBUTING**: a short file with the test commands from `CLAUDE.md` and the integration-test environment variables.

Not planned: `.gitlab-ci.yml` (the project lives on GitHub), splitting `admin.go`, replacing index-based API addressing.

---

## Decisions for the owner

1. A3: accept the recommended meaning of PostgreSQL `permissions` (database-level privileges only), or implement schema-level grants for a non-owner user instead. The second is a feature, not a fix.
2. C1: failed migrations stop and wait for a resubmit, instead of retrying forever.
3. D2: redacting the root password in API responses changes output that scripts may read. The owner's own `create-db.sh` scripts do not use that field.
4. G2: copyright line for `LICENSE`.
