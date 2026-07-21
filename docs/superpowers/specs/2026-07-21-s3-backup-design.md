# S3 Backup Upload — Design

## Summary

Backups (`backup.go` for Postgres/MariaDB, `mongo.go` for MongoDB) currently
write only to local disk (`{config_dir}/backups/{server-slug}/{database}/`).
This adds optional upload of each completed local backup to an S3 (or
S3-compatible, e.g. MinIO) bucket, with matching retention pruning and a
restore-time fallback when no local copy exists.

## Non-goals

- No S3-only mode — local backup files are always written first; S3 is a
  copy, not a replacement. (See "Local vs S3-only" decision below.)
- No per-database S3 targets — one S3 destination for the whole config.
- No credentials in `config.json` — S3 auth comes from the environment via
  the AWS SDK's default credential chain (env vars, shared config file, or
  IAM role), the same way `MYSQL_PWD` is passed via subprocess env today
  rather than stored in `BackupConfig`.
- No changes to local backup/prune/restore behavior when `s3` is absent
  from config — every S3 code path is skipped entirely if `Config.S3 ==
  nil`.

## Config

New optional top-level block in `config.json`, mirroring how `BackupConfig`
is already optional per-database:

```go
type S3Config struct {
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint,omitempty"` // custom endpoint (MinIO/S3-compatible); empty = real AWS
	Prefix   string `json:"prefix,omitempty"`   // optional key prefix under the bucket
}

type Config struct {
	Servers []DatabaseServer `json:"servers"`
	S3      *S3Config        `json:"s3,omitempty"`
}
```

No new per-database toggle: a database uploads to S3 whenever its existing
`backup.enabled` is `true` **and** `Config.S3 != nil`. Credentials
(`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` if
needed) and default region come from environment variables, consumed via
`awsconfig.LoadDefaultConfig(ctx)`. `S3Config.Region` overrides the SDK's
resolved region when set; `S3Config.Endpoint` set forces
`UsePathStyle: true` and a custom `BaseEndpoint` for MinIO/S3-compatible
targets.

## Dependency

Add `github.com/aws/aws-sdk-go-v2`, `.../config`, `.../service/s3` to
`go.mod`. Standard, actively maintained, supports custom endpoints for
S3-compatible services.

## New file: `s3.go`

- `newS3Client(ctx context.Context, cfg *S3Config) (*s3.Client, error)` —
  builds one client from `cfg`; called once per `runBackups` invocation
  and reused across all uploads/prunes/downloads in that run.
- `s3KeyPrefix(cfg *S3Config, serverSlug, database string) string` — joins
  `cfg.Prefix` (if set) + `serverSlug` + `database`, same segment shape as
  the local `backups/{server-slug}/{database}/` directory.
- `uploadToS3(ctx, client, cfg, serverSlug, database, localFile string) error`
  — `PutObject` of the local gzip file to
  `{keyPrefix}/{filename}`, filename unchanged from the local one (e.g.
  `mydb_2026-07-21.sql.gz`).
- `pruneS3Backups(ctx, client, cfg, serverSlug, database string, keepCount int) error`
  — `ListObjectsV2` under the key prefix, sort keys ascending (same
  date-prefixed-filename ordering trick `pruneBackups` already relies on),
  `DeleteObjects` for every key beyond the newest `keepCount`. No-op if
  `keepCount <= 0`, matching `pruneBackups`'s existing "0 = keep all"
  semantics.
- `downloadNewestFromS3(ctx, client, cfg, serverSlug, database, localDestDir string) (string, error)`
  — `ListObjectsV2` under the key prefix, pick the lexicographically
  newest key, `GetObject`, write it to `localDestDir/{filename}`, return
  the local path (or `""` if the prefix has no objects).

All four functions log and return an error rather than panicking; callers
treat S3 failures as non-fatal (see Wiring).

## Wiring

**`runBackups`** (`backup.go`): after a database's local backup succeeds
and local `pruneBackups` runs, if `config.S3 != nil`: build/reuse the S3
client, call `uploadToS3` then `pruneS3Backups`. On error, `log.Printf` and
continue to the next database — S3 problems never block local backups or
other databases.

**`mongoDBBackupSchedule`** (`mongo.go`): same pattern, after
`pruneMongoDBBackups`.

Client construction: `runBackups` builds the S3 client once (if
`config.S3 != nil`) and passes it down to both the Postgres/MariaDB loop
and `mongoDBBackupSchedule`, rather than reconnecting per database.

**`findNewestBackup`** (`backup.go`) and its MongoDB restore path: currently
take `configPath` and glob the local directory only. Both gain a
`config *Config` parameter (threaded from `processConfig`/`restoreDatabase`
callers, which already hold `*Config` in scope). If the local glob returns
no files and `config.S3 != nil`, fall back to `downloadNewestFromS3` into
the local backup directory (creating it if needed), then return that
downloaded path. If S3 is also empty or unconfigured, behavior is
unchanged (`""`, restore skipped, matching today's log message).

## Error handling

- Missing/invalid S3 credentials or unreachable endpoint: logged per
  operation, that database's S3 upload/prune/restore-fallback is skipped;
  local backup and restore-from-local continue working normally.
- Partial `DeleteObjects` failures during prune: log which keys failed,
  don't retry (matches local `pruneBackups`, which also just logs on
  `os.Remove` failure).

## Testing

`s3_test.go`, pure logic, no real S3 calls (network calls to S3 are out of
scope for unit tests — matches the project's existing style of not
integration-testing `pg_dump`/`mysqldump` execution):

- `s3KeyPrefix`: with/without `cfg.Prefix`, confirms segment join and
  slugging match the local path convention.
- Prune key-selection: given a list of object keys (date-prefixed
  filenames) and a `keepCount`, confirm the correct subset is selected for
  deletion (mirrors the sort-then-slice logic without hitting S3).
- Newest-key selection: given an unsorted list of keys, confirm the
  lexicographically newest is picked (same logic `downloadNewestFromS3`
  uses).
