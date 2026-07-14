package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
)

var configMu sync.RWMutex

type BackupConfig struct {
	Enabled         bool   `json:"enabled"`
	Schedule        string `json:"schedule"` // "daily" or "weekly"
	KeepCount       int    `json:"keep_count"`
	RestoreOnCreate bool   `json:"restore_on_create"` // restore newest backup when db is newly created
}

type DatabaseConfig struct {
	Database    string        `json:"database"`
	User        string        `json:"user"`
	Password    string        `json:"password"`
	Permissions []string      `json:"permissions"`
	Extensions  []string      `json:"extensions,omitempty"`
	Backup      *BackupConfig `json:"backup,omitempty"`
}

type DatabaseServer struct {
	Name                 string           `json:"name"`
	RootConnectionString string           `json:"root_connection_string"`
	DryRun               bool             `json:"dry_run"`
	Databases            []DatabaseConfig `json:"databases"`
}

type Config struct {
	Servers []DatabaseServer `json:"servers"`
}

type DBType int

const (
	PostgreSQL DBType = iota
	MariaDB
	MongoDB
)

func detectDBType(connStr string) DBType {
	if strings.HasPrefix(connStr, "mongodb://") || strings.HasPrefix(connStr, "mongodb+srv://") {
		return MongoDB
	}
	if strings.HasPrefix(connStr, "mariadb://") || strings.HasPrefix(connStr, "mysql://") {
		return MariaDB
	}
	return PostgreSQL
}

func main() {
	log.Println("Database Provisioner starting (PostgreSQL, MariaDB, MongoDB)...")

	if os.Getenv("ADMIN_SITE") == "true" {
		adminUser := os.Getenv("ADMIN_USER")
		adminPass := os.Getenv("ADMIN_PASSWORD")
		if adminUser == "" || adminPass == "" {
			log.Fatal("ADMIN_SITE=true requires ADMIN_USER and ADMIN_PASSWORD to be set")
		}
		go startAdminServer(getConfigPath())
	}

	if os.Getenv("USE_KUBERNETES_SECRETS") == "true" {
		secretsManager = initK8sSecretsManager()
	}

	go startBackupScheduler(getConfigPath())

	watchMode := os.Getenv("WATCH_MODE")
	if watchMode == "true" {
		runWatchMode()
	} else {
		runOnce()
	}
}

func runOnce() {
	configMu.RLock()
	config, err := loadConfig()
	configMu.RUnlock()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if err := processConfig(config); err != nil {
		log.Fatalf("Failed to process config: %v", err)
	}

	log.Println("Database provisioning completed")
}

func runWatchMode() {
	log.Println("Running in WATCH MODE - will monitor config file for changes")

	configPath := getConfigPath()
	var lastModTime time.Time
	checkInterval := 10 * time.Second

	for {
		fileInfo, err := os.Stat(configPath)
		if err != nil {
			log.Printf("Error checking config file: %v", err)
			time.Sleep(checkInterval)
			continue
		}

		currentModTime := fileInfo.ModTime()

		if currentModTime.After(lastModTime) {
			log.Println("Config file changed, reprocessing...")
			lastModTime = currentModTime

			configMu.RLock()
			config, err := loadConfig()
			configMu.RUnlock()
			if err != nil {
				log.Printf("Failed to load config: %v", err)
				time.Sleep(checkInterval)
				continue
			}

			if err := processConfig(config); err != nil {
				log.Printf("Failed to process config: %v", err)
			} else {
				log.Println("Config processed successfully")
			}
		}

		time.Sleep(checkInterval)
	}
}

func getConfigPath() string {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "/config/config.json"
	}
	return configPath
}

func loadConfig() (*Config, error) {
	configPath := getConfigPath()

	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate configuration
	if len(config.Servers) == 0 {
		return nil, fmt.Errorf("at least one server configuration is required")
	}

	for i, server := range config.Servers {
		if server.RootConnectionString == "" {
			return nil, fmt.Errorf("server %d: root connection string is required", i)
		}
		if len(server.Databases) == 0 {
			return nil, fmt.Errorf("server %d (%s): at least one database configuration is required", i, server.Name)
		}
	}

	return &config, nil
}

func processConfig(config *Config) error {
	log.Println("========================================")
	log.Println("Backup configuration summary:")
	log.Println("| server | database | frequency |")
	log.Println("|---|---|---|")
	backupFound := false
	for serverIdx, server := range config.Servers {
		serverName := server.Name
		if serverName == "" {
			serverName = fmt.Sprintf("Server %d", serverIdx+1)
		}
		for _, db := range server.Databases {
			if db.Backup == nil {
				continue
			}
			frequency := "disabled"
			if db.Backup.Enabled {
				frequency = db.Backup.Schedule
				if frequency == "" {
					frequency = "daily"
				}
			}
			backupFound = true
			log.Printf("| %s | %s | %s |", serverName, db.Database, frequency)
		}
	}
	if !backupFound {
		log.Println("No backups configured")
	}
	log.Println("========================================")

	// Process each server
	for serverIdx, server := range config.Servers {
		serverName := server.Name
		if serverName == "" {
			serverName = fmt.Sprintf("Server %d", serverIdx+1)
		}

		log.Printf("========================================")
		log.Printf("Processing server: %s", serverName)
		if server.DryRun {
			log.Printf("MODE: DRY RUN (no changes will be applied)")
		}
		log.Printf("========================================")

		// Detect database type
		dbType := detectDBType(server.RootConnectionString)

		var connStr string
		if dbType == MariaDB {
			// Convert mariadb:// or mysql:// format to go-sql-driver/mysql DSN format
			// Expected input format: mysql://user:password@host:port/dbname
			// Expected output format: user:password@tcp(host:port)/dbname
			connStr = server.RootConnectionString
			connStr = strings.TrimPrefix(connStr, "mariadb://")
			connStr = strings.TrimPrefix(connStr, "mysql://")

			// Split by / to separate credentials+host from database name
			parts := strings.Split(connStr, "/")
			if len(parts) >= 2 {
				credHostPart := parts[0] // user:password@host:port
				dbName := parts[1]       // database name
				// Add @tcp() wrapper around the host:port part
				if strings.Contains(credHostPart, "@") {
					credAndHost := strings.Split(credHostPart, "@")
					credentials := credAndHost[0] // user:password
					hostPort := credAndHost[1]    // host:port
					connStr = credentials + "@tcp(" + hostPort + ")/" + dbName
				}
			}

			log.Printf("Detected MariaDB/MySQL connection for %s", serverName)
		} else if dbType == MongoDB {
			connStr = server.RootConnectionString
			log.Printf("Detected MongoDB connection for %s", serverName)
		} else {
			connStr = server.RootConnectionString
			log.Printf("Detected PostgreSQL connection for %s", serverName)
		}

		// MongoDB doesn't use database/sql, handle separately
		if dbType == MongoDB {
			// Process each database configuration for this server
			for i, dbConfig := range server.Databases {
				log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

				created, provErr := provisionMongoDB(server.RootConnectionString, dbConfig, server.DryRun)
				if provErr != nil {
					log.Printf("Failed to provision MongoDB database %s on %s: %v", dbConfig.Database, serverName, provErr)
					continue
				}

				if created && !server.DryRun && dbConfig.Backup != nil && dbConfig.Backup.RestoreOnCreate {
					restoreDatabase(server, dbConfig, getConfigPath())
				}

				log.Printf("Successfully provisioned MongoDB database: %s with user: %s on %s", dbConfig.Database, dbConfig.User, serverName)
			}

			log.Printf("Completed processing server: %s", serverName)
			continue
		}

		// Connect to SQL database as root
		var db *sql.DB
		var err error

		if dbType == MariaDB {
			db, err = connectWithRetry("mysql", connStr, 5, 5*time.Second)
		} else {
			db, err = connectWithRetry("postgres", connStr, 5, 5*time.Second)
		}

		if err != nil {
			log.Printf("Failed to connect to %s: %v", serverName, err)
			log.Printf("Skipping server: %s", serverName)
			continue
		}

		log.Printf("Connected to %s successfully", serverName)

		// Process each database configuration for this server
		for i, dbConfig := range server.Databases {
			log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

			var created bool
			var provErr error
			if dbType == MariaDB {
				created, provErr = provisionMariaDB(db, dbConfig, server.DryRun)
			} else {
				created, provErr = provisionPostgreSQL(db, connStr, dbConfig, server.DryRun)
			}
			if provErr != nil {
				log.Printf("Failed to provision database %s on %s: %v", dbConfig.Database, serverName, provErr)
				continue
			}

			if created && !server.DryRun && dbConfig.Backup != nil && dbConfig.Backup.RestoreOnCreate {
				restoreDatabase(server, dbConfig, getConfigPath())
			}

			log.Printf("Successfully provisioned database: %s with user: %s on %s", dbConfig.Database, dbConfig.User, serverName)
		}

		db.Close()
		log.Printf("Completed processing server: %s", serverName)
	}

	log.Printf("========================================")
	log.Printf("All servers processed")
	log.Printf("========================================")

	return nil
}

func connectWithRetry(driverName, connStr string, maxRetries int, delay time.Duration) (*sql.DB, error) {
	var db *sql.DB
	var err error

	for i := 0; i < maxRetries; i++ {

		db, err = sql.Open(driverName, connStr)
		if err != nil {
			log.Printf("Attempt %d/%d: Failed to open connection: %v", i+1, maxRetries, err)
			time.Sleep(delay)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()

		if err == nil {
			return db, nil
		}

		log.Printf("Attempt %d/%d: Failed to ping database: %v", i+1, maxRetries, err)
		db.Close()
		time.Sleep(delay)
	}

	return nil, fmt.Errorf("failed to connect after %d attempts: %w", maxRetries, err)
}

func provisionPostgreSQL(db *sql.DB, rootConnStr string, config DatabaseConfig, dryRun bool) (bool, error) {
	ctx := context.Background()

	// Check if user exists
	userExists, err := checkPostgreSQLUserExists(ctx, db, config.User)
	if err != nil {
		return false, fmt.Errorf("failed to check user existence: %w", err)
	}

	// Create user if it doesn't exist
	if !userExists {
		log.Printf("Creating user: %s", config.User)
		createUserSQL := fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s'",
			quoteIdentifier(config.User),
			escapeString(config.Password))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", createUserSQL)
		} else {
			if _, err := db.ExecContext(ctx, createUserSQL); err != nil {
				return false, fmt.Errorf("failed to create user: %w", err)
			}
			log.Printf("User %s created successfully", config.User)
		}
	} else {
		log.Printf("User %s already exists", config.User)
		// Update password if user exists
		updatePasswordSQL := fmt.Sprintf("ALTER USER %s WITH PASSWORD '%s'",
			quoteIdentifier(config.User),
			escapeString(config.Password))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", updatePasswordSQL)
		} else {
			if _, err := db.ExecContext(ctx, updatePasswordSQL); err != nil {
				return false, fmt.Errorf("failed to update user password: %w", err)
			}
			log.Printf("Password updated for user %s", config.User)
		}
	}

	// Check if database exists
	dbExists, err := checkPostgreSQLDatabaseExists(ctx, db, config.Database)
	if err != nil {
		return false, fmt.Errorf("failed to check database existence: %w", err)
	}

	// Create database if it doesn't exist
	if !dbExists {
		log.Printf("Creating database: %s", config.Database)
		createDbSQL := fmt.Sprintf("CREATE DATABASE %s OWNER %s",
			quoteIdentifier(config.Database),
			quoteIdentifier(config.User))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", createDbSQL)
		} else {
			if _, err := db.ExecContext(ctx, createDbSQL); err != nil {
				if !isCollationVersionMismatch(err) {
					return false, fmt.Errorf("failed to create database: %w", err)
				}
				log.Printf("Collation version mismatch on template1, refreshing collation version and retrying")
				if _, err := db.ExecContext(ctx, "ALTER DATABASE template1 REFRESH COLLATION VERSION"); err != nil {
					return false, fmt.Errorf("failed to refresh template1 collation version: %w", err)
				}
				if _, err := db.ExecContext(ctx, createDbSQL); err != nil {
					return false, fmt.Errorf("failed to create database after collation refresh: %w", err)
				}
			}
			log.Printf("Database %s created successfully", config.Database)
		}
	} else {
		log.Printf("Database %s already exists", config.Database)
		// Update owner if database exists
		alterOwnerSQL := fmt.Sprintf("ALTER DATABASE %s OWNER TO %s",
			quoteIdentifier(config.Database),
			quoteIdentifier(config.User))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", alterOwnerSQL)
		} else {
			if _, err := db.ExecContext(ctx, alterOwnerSQL); err != nil {
				return false, fmt.Errorf("failed to alter database owner: %w", err)
			}
			log.Printf("Owner of database %s set to %s", config.Database, config.User)
		}
	}

	// Grant privileges based on configuration
	var grantSQL string
	if len(config.Permissions) > 0 {
		// Use explicit permissions if provided
		permissionsStr := strings.Join(config.Permissions, ", ")
		grantSQL = fmt.Sprintf("GRANT %s ON DATABASE %s TO %s",
			permissionsStr,
			quoteIdentifier(config.Database),
			quoteIdentifier(config.User))
	} else {
		// Default to all privileges
		grantSQL = fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s",
			quoteIdentifier(config.Database),
			quoteIdentifier(config.User))
	}

	if dryRun {
		log.Printf("[DRY RUN] Would execute: %s", grantSQL)
	} else {
		if _, err := db.ExecContext(ctx, grantSQL); err != nil {
			return false, fmt.Errorf("failed to grant privileges: %w", err)
		}
	}

	if len(config.Extensions) > 0 {
		if err := provisionPostgreSQLExtensions(ctx, rootConnStr, config.Database, config.Extensions, dryRun); err != nil {
			return false, err
		}
	}

	return !dbExists, nil
}

func provisionPostgreSQLExtensions(ctx context.Context, rootConnStr, dbName string, extensions []string, dryRun bool) error {
	if dryRun {
		for _, ext := range extensions {
			log.Printf("[DRY RUN] Would execute on %s: CREATE EXTENSION IF NOT EXISTS %s", dbName, quoteIdentifier(ext))
		}
		return nil
	}

	u, err := url.Parse(rootConnStr)
	if err != nil {
		return fmt.Errorf("failed to parse connection string for extensions: %w", err)
	}
	u.Path = "/" + dbName
	dbConn, err := sql.Open("postgres", u.String())
	if err != nil {
		return fmt.Errorf("failed to open connection to %s for extensions: %w", dbName, err)
	}
	defer dbConn.Close()

	for _, ext := range extensions {
		createExtSQL := fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s", quoteIdentifier(ext))
		if _, err := dbConn.ExecContext(ctx, createExtSQL); err != nil {
			return fmt.Errorf("failed to create extension %s on %s: %w", ext, dbName, err)
		}
		log.Printf("Extension %s created/verified on database %s", ext, dbName)
	}
	return nil
}

func provisionMariaDB(db *sql.DB, config DatabaseConfig, dryRun bool) (bool, error) {
	ctx := context.Background()

	// Check if database exists before creating
	dbExists, err := checkMariaDBDatabaseExists(ctx, db, config.Database)
	if err != nil {
		return false, fmt.Errorf("failed to check database existence: %w", err)
	}

	// Create database if it doesn't exist
	log.Printf("Creating database if not exists: %s", config.Database)
	createDbSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		config.Database)

	if dryRun {
		log.Printf("[DRY RUN] Would execute: %s", createDbSQL)
	} else {
		if _, err := db.ExecContext(ctx, createDbSQL); err != nil {
			return false, fmt.Errorf("failed to create database: %w", err)
		}
	}

	// Check if user exists
	userExists, err := checkMariaDBUserExists(ctx, db, config.User)
	if err != nil {
		return false, fmt.Errorf("failed to check user existence: %w", err)
	}

	if !userExists {
		// Create user
		log.Printf("Creating user: %s", config.User)
		createUserSQL := fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'",
			escapeString(config.User),
			escapeString(config.Password))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", createUserSQL)
		} else {
			if _, err := db.ExecContext(ctx, createUserSQL); err != nil {
				return false, fmt.Errorf("failed to create user: %w", err)
			}
			log.Printf("User %s created successfully", config.User)
		}
	} else {
		log.Printf("User %s already exists, updating password", config.User)
		// Update password
		updatePasswordSQL := fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s'",
			escapeString(config.User),
			escapeString(config.Password))

		if dryRun {
			log.Printf("[DRY RUN] Would execute: %s", updatePasswordSQL)
		} else {
			if _, err := db.ExecContext(ctx, updatePasswordSQL); err != nil {
				return false, fmt.Errorf("failed to update user password: %w", err)
			}
			log.Printf("Password updated for user %s", config.User)
		}
	}

	// Grant privileges based on configuration
	var grantSQL string
	if len(config.Permissions) > 0 {
		// Use explicit permissions if provided
		permissionsStr := strings.Join(config.Permissions, ", ")
		grantSQL = fmt.Sprintf("GRANT %s ON `%s`.* TO '%s'@'%%'",
			permissionsStr,
			config.Database,
			escapeString(config.User))
	} else {
		// Default to all privileges
		grantSQL = fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'",
			config.Database,
			escapeString(config.User))
	}

	if dryRun {
		log.Printf("[DRY RUN] Would execute: %s", grantSQL)
	} else {
		if _, err := db.ExecContext(ctx, grantSQL); err != nil {
			return false, fmt.Errorf("failed to grant privileges: %w", err)
		}
	}

	// Flush privileges
	if dryRun {
		log.Printf("[DRY RUN] Would execute: FLUSH PRIVILEGES")
	} else {
		if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
			return false, fmt.Errorf("failed to flush privileges: %w", err)
		}
	}

	log.Printf("Granted privileges on database %s to user %s", config.Database, config.User)

	return !dbExists, nil
}

func checkPostgreSQLUserExists(ctx context.Context, db *sql.DB, username string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)"
	err := db.QueryRowContext(ctx, query, username).Scan(&exists)
	return exists, err
}

func checkPostgreSQLDatabaseExists(ctx context.Context, db *sql.DB, database string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
	err := db.QueryRowContext(ctx, query, database).Scan(&exists)
	return exists, err
}

// isCollationVersionMismatch reports whether err is Postgres error XX000
// with a "collation version mismatch" message, raised against template1
// after the host's glibc/ICU collation library is upgraded out from under
// an existing cluster.
func isCollationVersionMismatch(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	return pqErr.Code == "XX000" && strings.Contains(pqErr.Message, "collation version mismatch")
}

func checkMariaDBUserExists(ctx context.Context, db *sql.DB, username string) (bool, error) {
	var count int
	query := "SELECT COUNT(*) FROM mysql.user WHERE user = ? AND host = '%'"
	err := db.QueryRowContext(ctx, query, username).Scan(&count)
	return count > 0, err
}

func checkMariaDBDatabaseExists(ctx context.Context, db *sql.DB, database string) (bool, error) {
	var count int
	query := "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?"
	err := db.QueryRowContext(ctx, query, database).Scan(&count)
	return count > 0, err
}

func quoteIdentifier(s string) string {
	return fmt.Sprintf(`"%s"`, s)
}

func escapeString(s string) string {
	// Basic SQL string escaping - replace single quotes with two single quotes
	result := ""
	for _, c := range s {
		if c == '\'' {
			result += "''"
		} else {
			result += string(c)
		}
	}
	return result
}
