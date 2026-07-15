# Database Provisioner

A Go-based application that automates PostgreSQL and MariaDB/MySQL database and user provisioning. It runs in a Docker container or as a systemd service, creating databases with dedicated users based on a configuration file. Ideal for homelab users who want to easily setup the databases.  

## Features

- **Multi-database support**: Works with PostgreSQL, MariaDB, MongoDB and MySQL
- **Multi-platform packages**: Pre-built `.deb` and `.rpm` packages for x86_64 and ARM64
- **Deployment flexibility**: Docker, Kubernetes ConfigMaps, or systemd services
- Creates PostgreSQL users with specified passwords
- Creates databases with specified owners
- Sets proper ownership and privileges
- Handles existing databases and users (updates passwords and ownership)
- Connection retry logic for reliability
- Configurable via JSON
- **Watch mode**: Continuously monitors config file for changes
- **Admin web UI**: Optional browser-based interface to add databases and change passwords
- **Automated backups**: Scheduled daily or weekly backups with configurable retention (PostgreSQL: `pg_dump`, MariaDB: `mysqldump`)
- **Auto-restore**: Optionally restores from the newest backup when a database is first created
- **Kubernetes native**: Works seamlessly with ConfigMaps
- **PostgreSQL extensions**: Automatically installs specified extensions into each database
- **Idempotent**: Safe to run multiple times
- **Full backup feature parity**: Both PostgreSQL and MariaDB support all backup features identically

## Configuration

Create a `config.json` file with your database server configurations. You can manage multiple database servers (PostgreSQL and/or MariaDB) in a single configuration file.

### Single Server Configuration

```json
{
  "servers": [
    {
      "name": "Main PostgreSQL",
      "root_connection_string": "postgres://postgres:rootpassword@postgres:5432/postgres?sslmode=disable",
      "databases": [
        {
          "database": "app_db",
          "user": "app_user",
          "password": "securepassword123"
        }
      ]
    }
  ]
}
```

### Multi-Server Configuration (PostgreSQL + MariaDB + MongoDB)

```json
{
  "servers": [
    {
      "name": "Production PostgreSQL",
      "root_connection_string": "postgres://postgres:rootpassword@postgres-prod:5432/postgres?sslmode=disable",
      "databases": [
        {
          "database": "app_db",
          "user": "app_user",
          "password": "securepassword123"
        },
        {
          "database": "analytics_db",
          "user": "analytics_user",
          "password": "analyticspass456"
        }
      ]
    },
    {
      "name": "Production MariaDB",
      "root_connection_string": "mariadb://root:rootpassword@mariadb-prod:3306/",
      "databases": [
        {
          "database": "wordpress_db",
          "user": "wordpress_user",
          "password": "wppass123"
        }
      ]
    },
    {
      "name": "Production MongoDB",
      "root_connection_string": "mongodb://admin:rootpassword@mongo-prod:27017/admin",
      "databases": [
        {
          "database": "app_db",
          "user": "app_user",
          "password": "securepassword123",
          "backup": {
            "enabled": true,
            "schedule": "daily",
            "keep_count": 7
          }
        }
      ]
    }
  ]
}
```

### Configuration Fields

- `servers`: Array of database server configurations
  - `name`: Friendly name for the server (used in logs and backup directory names)
  - `root_connection_string`: Database connection string with superuser credentials
    - PostgreSQL: `postgres://user:pass@host:port/database?sslmode=disable`
    - MariaDB: `mariadb://user:pass@host:port/` or `mysql://user:pass@host:port/`
    - MongoDB: `mongodb://user:pass@host:port/admin` or `mongodb+srv://user:pass@host/admin`
  - `databases`: Array of database configurations for this server
    - `database`: Name of the database to create
    - `user`: Username to create/manage
    - `password`: Password for the user
    - `extensions`: Optional list of PostgreSQL extensions to install in the database (e.g. `["uuid-ossp", "pgcrypto"]`). Each extension is created with `CREATE EXTENSION IF NOT EXISTS`. Not supported for MariaDB.
    - `backup`: Optional backup configuration (see [Backups](#backups))
      - `enabled`: `true` to enable scheduled backups
      - `schedule`: `"daily"` or `"weekly"` (weekly runs on Sundays at midnight)
      - `keep_count`: Number of backup files to retain (0 = keep all)
      - `restore_on_create`: `true` to restore from the newest backup when the database is first created

**Note**: The application automatically detects the database type for each server based on the connection string prefix. The `extensions` and `permissions` fields are PostgreSQL-only and are ignored for MariaDB and MongoDB servers.

### PostgreSQL Extensions

Add an `extensions` array to any PostgreSQL database entry to have those extensions installed automatically:

```json
{
  "database": "app_db",
  "user": "app_user",
  "password": "securepassword123",
  "extensions": ["uuid-ossp", "pgcrypto", "pg_trgm"]
}
```

Each extension is installed with `CREATE EXTENSION IF NOT EXISTS`, so it is safe to run repeatedly. Extensions are installed by the root/superuser connection and are available to the database user immediately after provisioning. This field is PostgreSQL-only and is ignored for MariaDB servers.

## Usage

### Using Docker Compose (Recommended)

**Single Database Server:**

1. Create your `config.json` file (see examples above)
2. Run the stack:

```bash
docker-compose up --build
```

**Multiple Database Servers (PostgreSQL + MariaDB):**

1. Create `config-multi.json` with multiple servers
2. Run with the multi-server compose file:

```bash
docker-compose -f docker-compose-multi.yml up --build
```

This will:
- Start PostgreSQL and MariaDB containers
- Build and run the provisioner
- Create all configured databases and users on both servers

### Using Docker Directly

1. Build the image:

```bash
docker build -t pg-provisioner .
```

2. Run the container (assuming PostgreSQL is accessible):

```bash
docker run -v $(pwd)/config.json:/config/config.json:ro pg-provisioner
```

### Using Kubernetes with ConfigMaps

The application supports Kubernetes ConfigMaps and can run in two modes:

#### 1. Deployment Mode (Continuous Watch)

Deploy as a long-running pod that watches for ConfigMap changes:

```bash
kubectl apply -f kubernetes-deployment.yaml
```

When you update the ConfigMap:
```bash
kubectl edit configmap pg-provisioner-config
```

The pod will automatically detect changes within ~10 seconds and reprocess the configuration.

**Note**: Kubernetes ConfigMap updates can take 60+ seconds to propagate to mounted volumes. For faster updates, consider using a Job or CronJob approach.

#### 2. Job Mode (Run Once)

Run as a one-time Job:

```bash
kubectl apply -f kubernetes-job.yaml
```

This is useful for:
- Initial database setup
- Scheduled provisioning with CronJobs
- CI/CD pipelines

#### 3. Using Secrets (Recommended for Production)

For sensitive credentials, use Kubernetes Secrets:

```bash
kubectl apply -f kubernetes-with-secrets.yaml
```

This approach:
- Stores connection strings and passwords in Secrets
- Uses an init container to interpolate values
- Keeps the ConfigMap for non-sensitive configuration

### Editing Config on the Fly

#### Docker Compose
Edit `config.json` and set `WATCH_MODE=true`:
```yaml
environment:
  WATCH_MODE: "true"
```
The container will detect changes within 10 seconds.

#### Kubernetes
Update the ConfigMap:
```bash
kubectl edit configmap pg-provisioner-config
# or
kubectl apply -f updated-config.yaml
```

The pod will automatically detect and apply changes.

### Running Locally

1. Install dependencies:

```bash
go mod download
```

2. Run the application:

```bash
# Run once
CONFIG_PATH=./config.json go run main.go

# Run in watch mode
WATCH_MODE=true CONFIG_PATH=./config.json go run main.go
```

## Connection String Format

### PostgreSQL
```
postgres://username:password@host:port/database?sslmode=disable
```

Example:
```
postgres://postgres:mypassword@localhost:5432/postgres?sslmode=disable
```

### MariaDB/MySQL
```
mariadb://username:password@host:port/
```
or
```
mysql://username:password@host:port/
```

Example:
```
mariadb://root:mypassword@localhost:3306/
```

**Note**: For MariaDB, you can use either `mariadb://` or `mysql://` as the protocol - both work the same way.

### MongoDB
```
mongodb://username:password@host:port/admin
```
or for Atlas / SRV records:
```
mongodb+srv://username:password@cluster.example.com/admin
```

Example:
```
mongodb://admin:mypassword@localhost:27017/admin
```

The root connection string must point to the `admin` database so the provisioner can create users and list databases. Standard MongoDB URI options (e.g. `?authSource=admin&tls=true`) are passed through unchanged.

## Behavior

- **Idempotent**: Safe to run multiple times
- **PostgreSQL**:
  - If a user exists: Updates the password
  - If a database exists: Updates the owner
  - Grants all privileges on the database to the user
- **MariaDB/MySQL**:
  - Creates database with UTF8MB4 character set
  - If a user exists: Updates the password
  - Grants all privileges on the database to the user unless specified
  - Automatically flushes privileges after changes
- **MongoDB**:
  - Connects to the `admin` database using the root connection string
  - Drops and recreates the user on each run to apply any password change (idempotent)
  - Grants the `readWrite` role on the target database
  - Creates a `{database}_data` collection on first run to materialize the database (MongoDB databases are created implicitly when data is first written)
- Includes connection retry logic (5 attempts with 5-second delays)
- Automatically detects database type from connection string

## Admin Web UI

An optional browser-based interface lets you add new database entries and change passwords without editing the config file manually. Changes are written to disk and picked up automatically when running in watch mode.

### Enabling the Admin UI

Set all three environment variables:

```bash
ADMIN_SITE=true ADMIN_USER=admin ADMIN_PASSWORD=secret
```

| Variable | Description |
|----------|-------------|
| `ADMIN_SITE` | Set to `true` to enable the admin server |
| `ADMIN_USER` | Username for HTTP Basic Auth |
| `ADMIN_PASSWORD` | Password for HTTP Basic Auth |
| `ADMIN_PORT` | Port to listen on (default: `8080`) |

### Example (Docker)

```bash
docker run \
  -e ADMIN_SITE=true \
  -e ADMIN_USER=admin \
  -e ADMIN_PASSWORD=secret \
  -e WATCH_MODE=true \
  -p 8080:8080 \
  -v $(pwd)/config.json:/config/config.json \
  pg-provisioner
```

Then open `http://localhost:8080` in your browser. You will be prompted for the username and password you set above.

### Features

- View all configured databases across all servers
- Change the password for any existing database user
- Add a new database entry to any server (with optional custom permissions)
- Edit each database's backup settings (enabled, schedule, keep count, restore-on-create) — see [Backups](#backups) for what these fields do

> **Note:** The admin UI is most useful with `WATCH_MODE=true`. In one-shot mode the process exits after the first run and the UI has no time to apply changes.

## Kubernetes Secrets Mode

When `USE_KUBERNETES_SECRETS=true`, the provisioner ignores the `password`
field in `config.json` for every database entry. Instead, for each database
it gets-or-creates a Kubernetes `Secret` containing a randomly generated
password, and uses that password to create/update the database user. The
`password` field in `config.json` is never read or overwritten in this mode
— leave it blank or filled with a placeholder, it's ignored either way.

**This mode only works when the provisioner is running inside a Kubernetes
pod.** It uses in-cluster configuration (the pod's ServiceAccount token and
CA certificate) to talk to the Kubernetes API — there is no support for
running this mode via systemd or plain Docker outside a cluster.

### Secret naming

One Secret is created per database entry, named:

```
<slugified-server-name>-<slugified-database-name>-credentials
```

For example, a server named `"Main PostgreSQL"` with a database `"app_db"`
gets a Secret named `main-postgresql-app-db-credentials`. Each Secret has a
single key: `password`.

### Required RBAC

The pod's ServiceAccount needs permission to read and write Secrets in its
namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
subjects:
- kind: ServiceAccount
  name: homelab-db-provisioner
  namespace: default
roleRef:
  kind: Role
  name: homelab-db-provisioner-secrets
  apiGroup: rbac.authorization.k8s.io
```

`kubernetes-deployment.yaml`, `kubernetes-job.yaml`, and
`kubernetes-with-secrets.yaml` already include this Role/RoleBinding and a
`serviceAccountName` — just uncomment the `USE_KUBERNETES_SECRETS` env var
in whichever manifest you use.

**Security note:** RBAC can't restrict access by Secret name pattern, so
this grants the provisioner read/write access to *all* Secrets in its
namespace — the same trust level it already has via its root database
connection-string Secret. Run it in a dedicated namespace if you want
tighter isolation.

### Using the generated secret in your own app

Reference the generated Secret from your application's Deployment the same
way you'd reference any Kubernetes Secret:

```yaml
env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: main-postgresql-app-db-credentials
      key: password
```

Or inspect it directly:

```bash
kubectl get secret main-postgresql-app-db-credentials -n default \
  -o jsonpath='{.data.password}' | base64 -d
```

The admin UI (see [Admin Web UI](#admin-web-ui)) shows the exact secret name
for each database when this mode is enabled.

### Rotating a password

With the admin UI enabled, each database row shows its Secret name and a
**Rotate** button. Clicking it generates a new password and updates the
Secret in place — the provisioner picks up the new password on its next
reconcile pass (immediately in watch mode). There is no automatic rotation
schedule; rotation is manual.

## Backups

The provisioner can automatically back up databases using `pg_dump` (PostgreSQL), `mysqldump` (MariaDB/MySQL), or `mongodump` (MongoDB). All three support:
- **Scheduled backups**: Daily or weekly automation
- **Compression**: All backups are gzip-compressed
- **Retention management**: Automatic pruning of old backups
- **Auto-restore**: Restoring newest backup when a database is first created
- **Incremental updates**: Backups run seamlessly without blocking provisioning

Backups are compressed with gzip and stored next to the config file.

> **MongoDB tools**: `mongodump` and `mongorestore` must be installed separately. They are not bundled in the Docker image. Install the [MongoDB Database Tools](https://www.mongodb.com/try/download/database-tools) package on the host or add it to a custom image layer.

### Directory Structure

Backups are written to a `backups/` subdirectory alongside `config.json`, organized by server name and database:

```
backups/
  production-postgresql/
    app_db/
      app_db_2024-01-15.sql.gz
      app_db_2024-01-16.sql.gz
    analytics_db/
      analytics_db_2024-01-15.sql.gz
  production-mariadb/
    wordpress_db/
      wordpress_db_2024-01-15.sql.gz
  production-mongodb/
    app_db/
      app_db_2024-01-15.archive.gz
      app_db_2024-01-16.archive.gz
```

MongoDB backups use the `.archive.gz` extension (mongodump archive format) rather than `.sql.gz`.

### Scheduling

- **Daily**: runs at midnight every day
- **Weekly**: runs at midnight every Sunday

Backups only run while the provisioner is running. Use `WATCH_MODE=true` (or a Kubernetes Deployment) to keep it running long-term.

### Configuration Example

```json
{
  "servers": [
    {
      "name": "Production PostgreSQL",
      "root_connection_string": "postgres://postgres:rootpassword@postgres:5432/postgres?sslmode=disable",
      "databases": [
        {
          "database": "app_db",
          "user": "app_user",
          "password": "securepassword123",
          "backup": {
            "enabled": true,
            "schedule": "daily",
            "keep_count": 7,
            "restore_on_create": true
          }
        }
      ]
    }
  ]
}
```

### Auto-Restore on Create

When `restore_on_create` is `true`, the provisioner will automatically restore the newest available backup into a database the first time it is created. This is useful for:

- Migrating a database to a new server
- Recreating a database from scratch
- Spinning up a fresh environment pre-populated with data

The restore is skipped silently if no backup files exist yet.

### Docker: Persisting Backups

Mount a host directory so backups survive container restarts:

```bash
docker run \
  -e WATCH_MODE=true \
  -v $(pwd)/config.json:/config/config.json \
  -v $(pwd)/backups:/config/backups \
  pg-provisioner
```

### Kubernetes: Persisting Backups

Add a PersistentVolumeClaim and mount it at the same path as the config directory so the `backups/` subdirectory is preserved across pod restarts.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `/config/config.json` | Path to configuration file |
| `WATCH_MODE` | `false` | `true` to monitor config file and reprocess on changes |
| `ADMIN_SITE` | — | Set to `true` to enable the admin web UI |
| `ADMIN_USER` | — | Basic Auth username for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PASSWORD` | — | Basic Auth password for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PORT` | `8080` | Port for the admin web UI |
| `USE_KUBERNETES_SECRETS` | `false` | `true` to generate per-database passwords into Kubernetes Secrets instead of using `config.json` passwords. Requires running inside a Kubernetes pod. See [Kubernetes Secrets Mode](#kubernetes-secrets-mode). |

## Security Considerations

1. **Protect config.json**: Contains sensitive credentials
2. **Use strong passwords**: Especially for the root connection
3. **Enable SSL**: In production, use `sslmode=require` in connection strings
4. **Limit network access**: Don't expose PostgreSQL directly to the internet
5. **Use secrets management**: Consider using Docker secrets or environment variables for sensitive data

## Building Packages

GitHub Actions automatically builds packages for both x86_64 and arm64 architectures when you push a tag (e.g., `v1.0.0`):

### Automated Package Builds

Push a semantic version tag to trigger the workflow:

```bash
git tag v0.3.0
git push origin v0.3.0
```

The `.github/workflows/packages.yml` workflow will:
1. Build Go binaries for `amd64` and `arm64`
2. Generate Debian packages (`.deb`) using nfpm
3. Generate RPM packages (`.rpm`) using nfpm
4. Create a GitHub Release with all four artifacts attached

### Artifacts

After a tagged release, you'll have:
- `homelab-db-provisioner_0.3.0_amd64.deb`
- `homelab-db-provisioner_0.3.0_arm64.deb`
- `homelab-db-provisioner_0.3.0_x86_64.rpm`
- `homelab-db-provisioner_0.3.0_aarch64.rpm`

### Installation from Package

```bash
# Debian/Ubuntu
sudo dpkg -i homelab-db-provisioner_0.3.0_amd64.deb

# RHEL/CentOS/Fedora
sudo rpm -i homelab-db-provisioner_0.3.0_x86_64.rpm
```

The package installs:
- Binary to `/usr/local/bin/homelab-db-provisioner`
- Systemd service files (oneshot and continuous)
- Example environment config to `/etc/homelab-db-provisioner/env.example`

## Error Handling

The application includes comprehensive error handling:
- Connection retry logic
- Validation of configuration
- Detailed logging of operations
- Continues processing remaining databases if one fails

## Logs

The application provides detailed logging:
- **Backup Summary**: At the beginning of each run, displays a table of all configured backups with server, database, and frequency
- **Connection status**: When connecting to each server
- **User creation/update**: When users are created or passwords updated
- **Database creation/update**: When databases are created or owners changed
- **Backup operations**: When backups are created or pruned
- **Restore operations**: When databases are restored from backups
- **Error messages**: With detailed context

### Example Output

```
========================================
Backup configuration summary:
| server | database | frequency |
|---|---|---|
| Production PostgreSQL | app_db | daily |
| Production PostgreSQL | analytics_db | weekly |
| Production MariaDB | wordpress_db | daily |
No backups configured
========================================
```

## License

MIT License
