# Admin UI Backup Configuration — Design

## Summary

The admin web UI (`admin.go`) currently has no way to view or edit a
database's backup settings (`enabled`, `schedule`, `keep_count`,
`restore_on_create`) — they can only be set by hand-editing `config.json`.
This adds:

1. A **Backup** column to the existing database table, with an
   always-visible inline form per row to view/edit that database's backup
   config.
2. The same four fields added to the existing **Add Database** form, so a
   newly added database can have backup config set at creation time.

## Non-goals

- No changes to `backup.go`'s scheduling, execution, or pruning logic —
  `BackupConfig` is an existing struct and `runBackups`/`findNewestBackup`
  already consume it exactly as-is.
- No JavaScript — the admin UI is plain server-rendered HTML today and
  stays that way.
- No new backup-related fields beyond the four that already exist on
  `BackupConfig` (`Enabled`, `Schedule`, `KeepCount`, `RestoreOnCreate`).

## UI

**Backup column** (new, added to the existing table alongside
Database/User/Permissions/Password-or-Secret): an always-visible inline
form per row, matching the existing per-row form pattern used for
password/rotate:

- `Enabled` — checkbox
- `Schedule` — `<select>` with `daily`/`weekly`
- `Keep Count` — number input (0 = keep all, matching existing
  `pruneBackups` semantics)
- `Restore on Create` — checkbox
- `Save` button → `POST /update-backup`

Pre-filled from the database's current `Backup` field if set; if `Backup`
is `nil`, the form shows defaults (`Enabled` unchecked, `Schedule`
`daily`, `Keep Count` `0`, `Restore on Create` unchecked).

**Add Database form**: the same four fields are added, all optional. If
`Enabled` is left unchecked and no other backup field is filled in, the
new database's `Backup` field stays `nil` (matching today's behavior when
a database is added without a `backup` block in `config.json`).

## Backend

**New handler `handleUpdateBackup(configPath string) http.HandlerFunc`**,
registered as `POST /update-backup` in `newAdminHandler`. Mirrors the
existing shape of `handleUpdatePassword`:

1. Parse `server_index`/`db_index` from the form; 400 on invalid or
   out-of-range values (same as existing handlers).
2. Parse the four backup fields from the form into a `*BackupConfig`.
3. Acquire `configMu.Lock()`, read + unmarshal `config.json`, set
   `cfg.Servers[si].Databases[di].Backup` to the parsed value, marshal +
   write the file (`0600`, same as existing writes), release the lock.
4. Redirect to `/` with a flash message (`"Backup settings updated"` or
   `"Error: ..."` on failure), matching existing handler conventions.

**`Keep Count` parsing**: `strconv.Atoi`; on parse failure, default to `0`
(keep all) rather than erroring the request — this matches the existing
"0 = keep all" semantics already documented for `keep_count` and avoids a
new failure mode for a cosmetic input mistake.

**Add Database handler (`handleAddDatabase`)**: extended to also parse the
same four fields and attach a `*BackupConfig` to the new `DatabaseConfig`
under the same nil-if-untouched rule as above.

## Testing

Table-driven tests in `admin_test.go`, mirroring the existing
`TestUpdatePassword_*`/`TestAddDatabase_*` structure:

- Enable backup on an existing database (previously `nil` `Backup`) —
  confirm `config.json` gets a populated `BackupConfig`.
- Disable an existing enabled backup — confirm `Enabled` flips to `false`
  (config block stays present, per the "always non-nil once touched" rule).
- Change schedule from daily to weekly on an existing entry.
- Add a new database via the Add Database form with backup fields filled
  in — confirm the new entry's `Backup` is populated correctly.
- Add a new database via the Add Database form with backup fields left at
  defaults — confirm the new entry's `Backup` stays `nil`.
- Invalid `server_index`/`db_index` on `/update-backup` — 400, matching
  existing handlers.
