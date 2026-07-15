# JSON API for CRUD over curl

## Purpose
Admin UI is HTML-form-only. User wants to script CRUD (create/read/update/delete) against servers and databases using curl + the existing `ADMIN_USER`/`ADMIN_PASSWORD` Basic Auth credentials, with JSON in and out.

## Design

New file `api.go`, package `main`. Routes are registered on the same `http.ServeMux` built in `newAdminHandler` (`admin.go`), so they inherit the existing `basicAuth` wrapper and are only reachable when `ADMIN_SITE=true`. No new dependencies — Go 1.25's stdlib `ServeMux` already supports method + wildcard patterns (`"GET /api/servers/{si}"`).

### Routes

```
GET    /api/servers                      list all servers
GET    /api/servers/{si}                 get one server
POST   /api/servers                      create server
PATCH  /api/servers/{si}                 update server
DELETE /api/servers/{si}                 delete server (and its databases)

GET    /api/servers/{si}/databases       list databases on a server
GET    /api/servers/{si}/databases/{di}  get one database
POST   /api/servers/{si}/databases       create database
PATCH  /api/servers/{si}/databases/{di}  update database
DELETE /api/servers/{si}/databases/{di}  delete database entry
```

`{si}` and `{di}` are array indices into `Config.Servers` / `Server.Databases` — the same convention the existing HTML forms use (`server_index`, `db_index` form fields). Not stable IDs: an index shifts if an earlier entry is deleted. This is an accepted limitation, consistent with how the HTML admin already works.

### Request/response shapes

**Server (response, GET/POST/PATCH echo):**
```json
{
  "index": 0,
  "name": "Prod Postgres",
  "root_connection_string": "postgres://root:pass@host:5432/postgres",
  "dry_run": false,
  "databases": [ /* array of Database, see below */ ]
}
```
List endpoint (`GET /api/servers`) returns `{"servers": [...]}`.

**Server create request** (`POST /api/servers`): `name` and `root_connection_string` required; `dry_run` optional (default `false`). New server always starts with `databases: []`.
```json
{"name": "Prod Postgres", "root_connection_string": "postgres://...", "dry_run": false}
```

**Server update request** (`PATCH /api/servers/{si}`): all fields optional, only provided fields change.
```json
{"name": "Prod Postgres 2", "dry_run": true}
```

**Database (response, GET/POST/PATCH echo)** — `password` is never included in responses, matching the HTML UI (which also never displays a stored password):
```json
{
  "index": 0,
  "database": "mydb",
  "user": "myuser",
  "permissions": ["SELECT", "INSERT"],
  "extensions": ["uuid-ossp"],
  "backup": {"enabled": true, "schedule": "daily", "keep_count": 7, "restore_on_create": false}
}
```

**Database create request** (`POST /api/servers/{si}/databases`): `database`, `user`, `password` required; `permissions`, `extensions`, `backup` optional.
```json
{"database": "mydb", "user": "myuser", "password": "s3cret", "permissions": ["SELECT", "INSERT"]}
```

**Database update request** (`PATCH /api/servers/{si}/databases/{di}`): all fields optional. Sending `password` rotates it (same effect as the HTML "Update Password" form). Sending `permissions`, `extensions`, or `backup` replaces the whole field (not merged).
```json
{"password": "newsecret", "backup": {"enabled": true, "schedule": "weekly", "keep_count": 4}}
```

### Error format

Non-2xx responses: `{"error": "human readable message"}`. Status codes:
- 400: malformed JSON body, missing required field
- 404: `{si}`/`{di}` out of range
- 405: wrong HTTP method (falls out naturally from `ServeMux` method patterns)
- 500: config read/write/marshal failure

### Delete semantics

DELETE removes the entry from `config.json` only — same "config is the source of truth, the watch loop reconciles reality to it" model the rest of the app already uses (adding a server/database doesn't synchronously create it either; the next watch-mode tick does). It does **not** run `DROP DATABASE`/`DROP USER` against the real server. Actual data deletion is a separate, more dangerous concern intentionally left out of scope. Deleting a server also removes all of its nested databases from config.

### Concurrency

Every handler follows the existing pattern from `admin.go`: acquire `configMu.Lock()` (write endpoints) or `configMu.RLock()` (read endpoints), read `config.json` from disk, mutate/marshal, write back with `json.MarshalIndent` + `os.WriteFile(path, out, 0600)`.

### README

New "## API" section with a curl example per route, using `curl -u "$ADMIN_USER:$ADMIN_PASSWORD"` and showing example JSON request/response bodies.

## Testing
New `api_test.go`, following the existing `admin_test.go` pattern (`makeTestConfig`, `newAdminHandler`, `httptest`). One success + one error-path test per route at minimum.
