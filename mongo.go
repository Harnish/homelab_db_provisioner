package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func provisionMongoDB(connStr string, config DatabaseConfig, dryRun bool) (bool, error) {
	if err := validateDatabaseConfig(MongoDB, config); err != nil {
		return false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connStr))
	if err != nil {
		// Connect only validates the URI; an unreachable server shows up below.
		return false, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	defer client.Disconnect(ctx)

	names, err := client.ListDatabaseNames(ctx, bson.M{"name": config.Database})
	if err != nil {
		return false, fmt.Errorf("failed to list databases: %w: %w", errServerUnreachable, err)
	}
	dbExists := len(names) > 0

	if dryRun {
		log.Printf("[DRY RUN] Would create or update user %s for database %s", config.User, config.Database)
		return !dbExists, nil
	}

	// Update the user in place when it exists: dropping and recreating it
	// would leave the application without an account between the two commands.
	adminDB := client.Database("admin")
	var users struct {
		Users []bson.M `bson:"users"`
	}
	if err := adminDB.RunCommand(ctx, bson.D{{Key: "usersInfo", Value: config.User}}).Decode(&users); err != nil {
		return false, fmt.Errorf("failed to look up user: %w", err)
	}
	verb := "createUser"
	if len(users.Users) > 0 {
		verb = "updateUser"
	}
	err = adminDB.RunCommand(ctx, bson.D{
		{Key: verb, Value: config.User},
		{Key: "pwd", Value: config.Password},
		{Key: "roles", Value: bson.A{bson.M{"role": "readWrite", "db": config.Database}}},
	}).Err()
	if err != nil {
		return false, fmt.Errorf("%s failed: %w", verb, err)
	}
	log.Printf("MongoDB user %s ready for database %s (%s)", config.User, config.Database, verb)

	// MongoDB creates a database on first write, so insert a marker document.
	collectionName := config.Database + "_data"
	_, err = client.Database(config.Database).Collection(collectionName).
		InsertOne(ctx, bson.M{"_id": "provisioned", "created_at": time.Now()})
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return false, fmt.Errorf("failed to create collection: %w", err)
	}
	log.Printf("Collection %s created/verified in database %s", collectionName, config.Database)

	return !dbExists, nil
}

// mongoToolURI moves the database in a root URI (normally /admin) into
// authSource. mongodump and mongorestore refuse a --db that differs from the
// database named in the URI.
func mongoToolURI(connStr string) string {
	base, query, _ := strings.Cut(connStr, "?")
	i := strings.Index(base, "://")
	if i < 0 {
		return connStr
	}
	slash := strings.Index(base[i+3:], "/")
	if slash < 0 {
		return connStr
	}
	db := base[i+3+slash+1:]
	base = base[:i+3+slash+1]
	if db != "" && !strings.Contains(query, "authSource=") {
		if query != "" {
			query += "&"
		}
		query += "authSource=" + db
	}
	if query != "" {
		return base + "?" + query
	}
	return base
}

// mongoToolConfig writes connStr to a private temp config file for
// mongodump/mongorestore, keeping the root password off the command line
// (where any local user could read it from the process list). The caller
// removes the file.
func mongoToolConfig(connStr string) (string, error) {
	f, err := os.CreateTemp("", "mongo-tool-*.yaml") // created 0600
	if err != nil {
		return "", err
	}
	defer f.Close()
	// A JSON string is a valid YAML double-quoted scalar.
	if _, err := fmt.Fprintf(f, "uri: %s\n", strconv.Quote(mongoToolURI(connStr))); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func runMongoTool(tool, connStr string, args ...string) error {
	cfg, err := mongoToolConfig(connStr)
	if err != nil {
		return fmt.Errorf("%s: write config: %w", tool, err)
	}
	defer os.Remove(cfg)

	cmd := exec.Command(tool, append([]string{"--config", cfg}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w: %s", tool, err, stderr.String())
	}
	return nil
}

func backupMongoDB(connStr, database, destFile string) error {
	if err := runMongoTool("mongodump", connStr, "--db", database, "--archive="+destFile, "--gzip"); err != nil {
		return err
	}
	if err := os.Chmod(destFile, 0600); err != nil {
		return fmt.Errorf("chmod %s: %w", destFile, err)
	}
	log.Printf("MongoDB backup created: %s", destFile)
	return nil
}

func restoreMongoDB(connStr, database, backupFile string) error {
	// --drop: replace existing collections rather than merging into them.
	if err := runMongoTool("mongorestore", connStr, "--db", database, "--archive="+backupFile, "--gzip", "--drop"); err != nil {
		return err
	}
	log.Printf("MongoDB restore completed for database: %s", database)
	return nil
}

// backupMongoServer backs up one MongoDB server's databases. Called from
// runBackups in backup.go.
func backupMongoServer(config *Config, server DatabaseServer, configPath string, t time.Time, catchUp bool, s3Client *s3.Client) {
	backupBase := filepath.Join(filepath.Dir(configPath), "backups")

	for _, db := range server.Databases {
		if !backupDue(configPath, server.Name, db, t, catchUp) {
			continue
		}

		dir := filepath.Join(backupBase, slugify(server.Name), db.Database)
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Printf("backup: mkdir %s: %v", dir, err)
			continue
		}

		filename := filepath.Join(dir, fmt.Sprintf("%s_%s.archive.gz", db.Database, t.Format("2006-01-02")))

		if err := backupMongoDB(server.RootConnectionString, db.Database, filename); err != nil {
			log.Printf("backup: MongoDB %s/%s: %v", server.Name, db.Database, err)
			os.Remove(filename)
			continue
		}

		log.Printf("backup: created %s", filename)
		if db.Backup.KeepCount > 0 {
			pruneBackups(dir, db.Database, ".archive.gz", db.Backup.KeepCount)
		}

		if config.S3 != nil && s3Client != nil {
			ctx := context.Background()
			slug := slugify(server.Name)
			if err := uploadToS3(ctx, s3Client, config.S3, slug, db.Database, filename); err != nil {
				log.Printf("backup: s3 upload %s/%s: %v", server.Name, db.Database, err)
			} else if err := pruneS3Backups(ctx, s3Client, config.S3, slug, db.Database, db.Backup.KeepCount); err != nil {
				log.Printf("backup: s3 prune %s/%s: %v", server.Name, db.Database, err)
			}
		}
	}
}
