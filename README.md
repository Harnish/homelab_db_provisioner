# PostgreSQL Database Provisioner

A Go-based application that automates PostgreSQL and MariaDB/MySQL database and user provisioning. It runs in a Docker container and creates databases with dedicated users based on a configuration file.  Making it super easy for homelab users to setup the databases.  

## Features

- Creates PostgreSQL users with specified passwords
- Creates databases with specified owners
- Sets proper ownership and privileges
- Handles existing databases and users (updates passwords and ownership)
- Connection retry logic for reliability
- Configurable via JSON
- **Watch mode**: Continuously monitors config file for changes
- **Admin web UI**: Optional browser-based interface to add databases and change passwords
- **Kubernetes native**: Works seamlessly with ConfigMaps
- **Idempotent**: Safe to run multiple times
- **Multi-database support**: Works with both PostgreSQL and MariaDB/MySQL

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

### Multi-Server Configuration (PostgreSQL + MariaDB)

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
      "name": "Development PostgreSQL",
      "root_connection_string": "postgres://postgres:devpassword@postgres-dev:5432/postgres?sslmode=disable",
      "databases": [
        {
          "database": "test_db",
          "user": "test_user",
          "password": "testpass123"
        }
      ]
    }
  ]
}
```

### Configuration Fields

- `servers`: Array of database server configurations
  - `name`: Friendly name for the server (used in logs)
  - `root_connection_string`: Database connection string with superuser credentials
    - PostgreSQL: `postgres://user:pass@host:port/database?sslmode=disable`
    - MariaDB: `mariadb://user:pass@host:port/` or `mysql://user:pass@host:port/`
  - `databases`: Array of database configurations for this server
    - `database`: Name of the database to create
    - `user`: Username to create/manage
    - `password`: Password for the user

**Note**: The application automatically detects the database type for each server based on the connection string prefix.

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

> **Note:** The admin UI is most useful with `WATCH_MODE=true`. In one-shot mode the process exits after the first run and the UI has no time to apply changes.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `/config/config.json` | Path to configuration file |
| `WATCH_MODE` | `false` | `true` to monitor config file and reprocess on changes |
| `ADMIN_SITE` | — | Set to `true` to enable the admin web UI |
| `ADMIN_USER` | — | Basic Auth username for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PASSWORD` | — | Basic Auth password for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PORT` | `8080` | Port for the admin web UI |

## Security Considerations

1. **Protect config.json**: Contains sensitive credentials
2. **Use strong passwords**: Especially for the root connection
3. **Enable SSL**: In production, use `sslmode=require` in connection strings
4. **Limit network access**: Don't expose PostgreSQL directly to the internet
5. **Use secrets management**: Consider using Docker secrets or environment variables for sensitive data

## Error Handling

The application includes comprehensive error handling:
- Connection retry logic
- Validation of configuration
- Detailed logging of operations
- Continues processing remaining databases if one fails

## Logs

The application provides detailed logging:
- Connection status
- User creation/update
- Database creation/update
- Ownership and privilege changes
- Error messages with context

## License

MIT License
