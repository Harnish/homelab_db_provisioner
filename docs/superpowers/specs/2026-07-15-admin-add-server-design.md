# Admin UI: Add Database Server

## Purpose
Admin UI can add databases to existing servers but not add a new server. Add that.

## Design

New route `POST /add-server` in `admin.go`, mirrors `handleAddDatabase`.

**Form fields** (new "Add Server" fieldset, placed above "Add Database" in template):
- `name` (text, required)
- `root_connection_string` (text, required) — same prefixes as existing config (`postgres://`, `mariadb://`/`mysql://`, `mongodb://`)
- `dry_run` (checkbox)

**Handler behavior:**
- Parse form, validate `name` and `root_connection_string` non-empty.
- Read config under `configMu.Lock()`, append `DatabaseServer{Name, RootConnectionString, DryRun, Databases: []DatabaseConfig{}}`.
- Write config via `json.MarshalIndent` + `os.WriteFile` (0600), same as other handlers.
- Redirect to `/?msg=...` on success/error, matching existing flash-message pattern.

**Template:**
- New fieldset "Add Server" with the three fields above and a submit button.
- No changes needed to the existing "Add Database" dropdown — it already iterates `.Servers`, so a newly added server appears there on the next page load (handler redirects to `/`, which re-reads config).

## Known tradeoff
A newly added server has zero databases. `loadConfig()` requires ≥1 database per server, so until the admin adds the first database, the next watch-mode tick logs a load error and skips processing (does not crash — existing behavior for any load failure). Accepted; no validation change.

## Testing
No new Go test required — this is a straightforward mirror of the existing, already-covered `handleAddDatabase` handler pattern. Manual check: start admin UI, submit Add Server, confirm it appears in config file and in the Add Database dropdown.
