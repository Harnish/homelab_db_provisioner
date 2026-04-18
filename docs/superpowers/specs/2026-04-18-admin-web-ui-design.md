# Admin Web UI Design

**Date:** 2026-04-18
**Topic:** Admin web interface for adding databases and changing passwords

## Overview

Add a simple HTTP admin interface to the db-provisioner binary. It is gated behind environment variables and modifies the config file on disk, letting watch mode apply changes to the actual database servers.

## Activation

The admin server only starts when all three environment variables are present:

- `ADMIN_SITE=true`
- `ADMIN_USER=<username>`
- `ADMIN_PASSWORD=<password>`

If `ADMIN_SITE=true` but either `ADMIN_USER` or `ADMIN_PASSWORD` is missing, the application logs a fatal error and exits at startup.

The admin server listens on the port specified by `ADMIN_PORT` (default: `8080`).

## Architecture

A new `admin.go` file contains the HTTP server and is started as a goroutine from `main()` alongside the existing run modes (once or watch). It runs for the lifetime of the process.

A `sync.RWMutex` (`configMu`) is introduced to guard all config file access:
- Watch mode uses `RLock` when reading the config file.
- Admin POST handlers use `Lock` when writing the config file.

## Routes

All routes are protected by HTTP Basic Auth middleware using `ADMIN_USER` and `ADMIN_PASSWORD`. Unauthenticated requests receive `401 WWW-Authenticate: Basic realm="Admin"` and no page content.

### `GET /`
Renders an HTML page showing:
- A table of all servers and their databases, with an inline "Change Password" form per database row.
- An "Add Database" form with fields:
  - Server (dropdown, populated from config)
  - Database name
  - Username
  - Password
  - Permissions (optional, comma-separated, e.g. `SELECT, INSERT`)

Flash messages (success/error) are shown via a `msg` query parameter.

### `POST /update-password`
Form fields: `server_index`, `db_index`, `new_password`

Reads the config file, updates the password for the specified database entry, writes the config file back, then redirects to `GET /?msg=Password+updated`.

### `POST /add-database`
Form fields: `server_index`, `database`, `user`, `password`, `permissions`

Reads the config file, appends a new `DatabaseConfig` to the selected server's `databases` list, writes the config file back, then redirects to `GET /?msg=Database+added`.

Duplicate database/user combinations are allowed — the provisioner is already idempotent.

## Error Handling

- If the config file cannot be read on a POST, redirect to `GET /?msg=Error:+<reason>` and make no changes.
- If the config file cannot be written after modification, redirect to `GET /?msg=Error:+<reason>`.
- Index bounds are validated on both POST handlers; out-of-range indices return a `400 Bad Request`.

## Files

- **`admin.go`** — HTTP server, Basic Auth middleware, route handlers, HTML template (embedded as a Go string).
- **`main.go`** — Modified to start the admin server goroutine when env vars are present; introduces `configMu` shared with `admin.go`.

## Non-Goals

- No JavaScript framework — plain HTML forms only.
- No live reload or WebSocket — watch mode handles applying changes.
- No delete operation in this iteration.
