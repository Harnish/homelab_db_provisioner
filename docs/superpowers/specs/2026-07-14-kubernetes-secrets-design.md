# Kubernetes Secrets for Generated DB Passwords — Design

## Summary

Add an opt-in `USE_KUBERNETES_SECRETS` environment variable. When `true`, the
provisioner ignores each database entry's `password` field in `config.json`
and instead manages a per-database Kubernetes `Secret` containing a
provisioner-generated password. That password is what gets set on the actual
database user. The admin UI surfaces the secret's name and shows the user how
to wire it into their own Deployment.

## Non-goals

- Does **not** work outside a Kubernetes pod (no kubeconfig fallback for
  systemd/Docker-standalone deployments). Those deployments must continue
  using plain `password` in `config.json`.
- Does not change backup/restore behavior — `pg_dump`/`mysqldump`/`mongodump`
  and restore paths authenticate with the server's root connection string,
  not the per-database user password, so they are unaffected.
- Does not auto-rotate passwords on a schedule. Rotation is manual, via the
  admin UI "Rotate" button.

## Architecture

New file `k8ssecrets.go`, package `main`.

- Uses `k8s.io/client-go` (in-cluster config only) + `k8s.io/apimachinery`.
  New dependencies added to `go.mod`.
- At startup, if `os.Getenv("USE_KUBERNETES_SECRETS") == "true"`: build a
  `*kubernetes.Clientset` via `rest.InClusterConfig()`. If this fails,
  `log.Fatal` immediately — same fail-fast style as the existing
  `ADMIN_SITE` env-var validation in `main.go`. A misconfigured deployment
  should not silently run with stale or empty passwords.
- Namespace is read once from
  `/var/run/secrets/kubernetes.io/serviceaccount/namespace` (the standard
  in-pod file — no new env var for this).
- The clientset and namespace are stored in package-level vars, initialized
  once in `main()`, reused by `processConfig()` on every watch-mode tick and
  by the admin UI's rotate handler.

## Reconciliation flow

Runs inside `processConfig()`, per database entry, immediately before the
existing call to `provisionPostgreSQL` / `provisionMariaDB` /
`provisionMongoDB` (applies uniformly since all three consume the same
`DatabaseConfig.Password` field):

1. Compute secret name: `<slugify(server.Name)>-<db.Database>-credentials`,
   reusing the existing `slugify()` helper from `backup.go` (currently only
   used for backup directory paths).
2. `Get` the secret in the pod's namespace.
   - **Found**: read the `password` key, overwrite the in-memory
     `dbConfig.Password` with it. No mutation, no rotation — idempotent,
     matching the "existing user → update password to configured value"
     pattern already used elsewhere in this codebase.
   - **Not found**: generate a new password (reuse `generatePassword()`,
     relocated from `admin.go` to `k8ssecrets.go` since it's now called
     from non-admin code too), `Create` a new `Opaque` Secret with a single
     key `password`, overwrite `dbConfig.Password` with the generated value.
3. Provisioning proceeds unchanged using the overridden `dbConfig.Password`.
4. `config.json` on disk is never read for, or overwritten with, the real
   password in this mode — whatever is in the `password` field there is
   simply ignored at runtime.
5. Failure to reach the k8s API for one database's secret: log and skip that
   database (`continue`), matching the existing per-database
   continue-on-error pattern in `processConfig()`. This is distinct from the
   startup-time in-cluster-config failure, which is fatal.

## Admin UI changes (`admin.go`)

Only when `USE_KUBERNETES_SECRETS=true`:

- The per-database table row's password column is replaced with:
  - The secret name (e.g. `main-postgresql-app_db-credentials`).
  - A single **Rotate** button (`POST /rotate-secret`, new handler) that
    generates a new password, `Update`s the existing Secret's `password`
    key, and redirects with a flash message. This replaces both the manual
    "Update password" and "Generate" forms for these rows — manual entry is
    hidden since the config field is ignored anyway.
- Below the table, a static (not per-row) help block showing:
  - `kubectl get secret <name> -n <namespace> -o jsonpath='{.data.password}' | base64 -d`
  - A YAML snippet showing `envFrom.secretRef` / `env.valueFrom.secretKeyRef`
    usage for referencing a generated secret from the user's own app
    Deployment.

When `USE_KUBERNETES_SECRETS` is unset/`false` (default), the admin UI is
byte-for-byte unchanged from today.

## RBAC & manifests

`kubernetes-deployment.yaml`, `kubernetes-job.yaml`, and
`kubernetes-with-secrets.yaml` each get a new `Role` + `RoleBinding` bound to
the existing `ServiceAccount`:

```yaml
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "create", "update", "patch"]
```

Namespace-scoped, not cluster-scoped. RBAC cannot restrict by name pattern,
so this grants access to all secrets in the namespace — the same trust level
the app already has via its root DB connection-string secret. Documented as
a security note in the README rather than worked around.

## Documentation (`README.md`)

- Add `USE_KUBERNETES_SECRETS` to the environment variables table.
- New "Kubernetes Secrets Mode" section covering:
  - What it does and when to use it (vs. plain `password` in config).
  - Secret naming scheme and content (`password` key only).
  - Required RBAC (link to the Role/RoleBinding snippet above).
  - How to reference the generated secret in your own application's
    Deployment (env var example).
  - How rotation works via the admin UI.
  - Explicit statement that this mode requires running inside Kubernetes
    (in-cluster config) — not supported for systemd/Docker-standalone.

## Testing

- Unit tests for secret-name derivation (slug + database → name).
- Unit tests for the get-or-create reconciliation logic using a fake
  clientset (`k8s.io/client-go/kubernetes/fake`) — covers: secret missing
  (created, password generated), secret present (reused, no mutation), API
  error (database skipped, others continue).
- Admin UI test: rotate handler patches the secret and leaves `config.json`
  untouched.
- Existing `go test ./...` must continue passing unmodified when
  `USE_KUBERNETES_SECRETS` is unset.
