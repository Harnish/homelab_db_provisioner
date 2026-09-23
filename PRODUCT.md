# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

A solo homelab operator running their own self-hosted services. They open the admin UI occasionally, not daily: when deploying a new app that needs a database, when checking that backups and migrations are healthy, or when something needs rotating or recovering. They are technical (they write the config file, run Kubernetes or systemd) but are not in the UI often enough to remember its details between visits.

## Product Purpose

DB Provisioner turns a declarative config file into real PostgreSQL, MariaDB/MySQL, and MongoDB databases, users, grants, and extensions, idempotently. It also runs scheduled backups with retention (local and optional S3), restores on create, migrates PostgreSQL databases between servers (for major-version upgrades), and manages per-database Kubernetes Secrets.

The optional admin UI (`ADMIN_SITE=true`) is a convenience layer over that config file. Success means the operator can:

- add a database for a new app and walk away with working credentials;
- see at a glance whether every server and database is healthy (backups, migration state, secret state);
- rotate passwords or secrets and run recovery or migration without hand-editing JSON.

## Positioning

The config file (`config.json` or a Kubernetes ConfigMap) is the source of truth, not the UI. Every UI action writes to that file, and the watch loop applies it. The UI never holds state the file doesn't.

## Operating Context

- Deployed as a Docker container, a Kubernetes Deployment/Job with ConfigMap, or a systemd service from `.deb`/`.rpm` packages (x86_64 and ARM64).
- Most edits happen by hand or through GitOps on the config file. The UI is for the moments when that's slower than a click.
- The UI is most useful with `WATCH_MODE=true`, so changes written by the UI get picked up within about 10 seconds.
- Access is behind HTTP Basic Auth (`ADMIN_USER` / `ADMIN_PASSWORD`) on `ADMIN_PORT` (default 8080).

## Capabilities and Constraints

- Server-rendered admin UI: one inline Go `html/template` in `admin.go` with form POSTs and redirect-with-flash-message. A JSON REST API (`/api/servers/...`) sits beside it.
- UI actions: view servers and databases; add a server; add a database; change or generate a password; rotate a Kubernetes Secret; edit backup settings; set or clear a migration.
- Terminology: *server* (a `DatabaseServer` with a root connection string), *database* (a `DatabaseConfig` entry), *migration tombstone* (a completed migrate entry that provisioning skips), *dry run* (per-server; SQL is logged, not executed).
- **Secrets are never shown in plain text.** Passwords and connection strings must not render in the UI. Flash messages travel in the `?msg=` URL, so they must never carry a secret.
- **Dangerous operations need guardrails.** Dropping the source after a migration, password changes, secret rotation, and deletes need explicit, deliberate confirmation. Current pattern: typing the database name back to drop the migration source (checked on the server), plus a confirmation step on Generate, Rotate, and Clear migration.
- Undecided: whether the UI must keep working without JavaScript, and whether it must be usable on a phone.

## Evidence on Hand

- `README.md` has the feature list and configuration examples.
- `config.json` and `config-multi.json` hold sample data. Use them for realistic UI states.
- No logo, brand assets, screenshots, or user testimonials exist. Don't fabricate any.

## Product Principles

1. **The file is the truth.** The UI reflects and edits the config; it never diverges from it or hides what it wrote.
2. **Status first.** An occasional visitor should see what's healthy, what's failing, and what's pending before they see any forms.
3. **Destructive means deliberate.** The more irreversible the action, the more explicit the confirmation.
4. **Credentials stay out of sight.** Point to where a secret lives (a K8s Secret, the config file). Don't display it.
5. **Built for the rare visit.** Labels and states should explain themselves to an operator who hasn't opened the UI in months.
