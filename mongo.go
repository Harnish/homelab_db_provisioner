package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func provisionMongoDB(connStr string, config DatabaseConfig, dryRun bool) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connStr))
	if err != nil {
		return false, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	defer client.Disconnect(ctx)

	// Check if database exists
	adminDB := client.Database("admin")
	var result bson.M
	err = adminDB.RunCommand(ctx, bson.M{"listDatabases": 1}).Decode(&result)
	if err != nil {
		return false, fmt.Errorf("failed to list databases: %w", err)
	}

	databases, ok := result["databases"].([]interface{})
	if !ok {
		return false, fmt.Errorf("unexpected database list format")
	}

	dbExists := false
	for _, dbItem := range databases {
		if dbMap, ok := dbItem.(bson.M); ok {
			if name, ok := dbMap["name"].(string); ok && name == config.Database {
				dbExists = true
				break
			}
		}
	}

	// Create user
	log.Printf("Creating or updating MongoDB user: %s for database: %s", config.User, config.Database)
	if dryRun {
		log.Printf("[DRY RUN] Would create user %s for database %s", config.User, config.Database)
	} else {
		// Remove existing user if present
		adminDB.RunCommand(ctx, bson.M{
			"dropUser":     config.User,
			"writeConcern": bson.M{"w": 0}, // Ignore errors if user doesn't exist
		})

		// Create new user
		err = adminDB.RunCommand(ctx, bson.M{
			"createUser": config.User,
			"pwd":        config.Password,
			"roles": []bson.M{
				{
					"role": "readWrite",
					"db":   config.Database,
				},
			},
		}).Err()
		if err != nil {
			return false, fmt.Errorf("failed to create user: %w", err)
		}
		log.Printf("User %s created/updated successfully", config.User)
	}

	// Create collection (creates the database implicitly if it doesn't exist)
	if !dryRun {
		targetDB := client.Database(config.Database)
		collectionName := config.Database + "_data"

		// Create collection with a document to ensure it exists
		collection := targetDB.Collection(collectionName)
		_, err := collection.InsertOne(ctx, bson.M{"_id": "provisioned", "created_at": time.Now()})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return false, fmt.Errorf("failed to create collection: %w", err)
		}
		log.Printf("Collection %s created/verified in database %s", collectionName, config.Database)
	}

	return !dbExists, nil
}

func backupMongoDB(connStr, database, destFile string) error {
	// MongoDB connection string contains all auth info needed for mongodump
	// The --uri flag handles credentials and connection details

	cmd := exec.Command("mongodump",
		"--uri", connStr,
		"--db", database,
		"--archive="+destFile,
		"--gzip",
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mongodump failed: %w: %s", err, stderr.String())
	}

	log.Printf("MongoDB backup created: %s", destFile)
	return nil
}

func restoreMongoDB(connStr, database, backupFile string) error {
	cmd := exec.Command("mongorestore",
		"--uri", connStr,
		"--db", database,
		"--archive="+backupFile,
		"--gzip",
		"--drop", // Drop existing collections before restoring
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mongorestore failed: %w: %s", err, stderr.String())
	}

	log.Printf("MongoDB restore completed for database: %s", database)
	return nil
}

// parseMongoDBConnStr extracts components from MongoDB connection string
// Returns a struct with connection details (currently unused but available for future use)
func parseMongoDBConnStr(connStr string) (map[string]string, error) {
	result := make(map[string]string)

	// MongoDB URIs can be:
	// mongodb://[username[:password]@]host[:port][/database][?options]
	// mongodb+srv://[username[:password]@]host/database[?options]

	if !strings.HasPrefix(connStr, "mongodb://") && !strings.HasPrefix(connStr, "mongodb+srv://") {
		return nil, fmt.Errorf("invalid MongoDB connection string")
	}

	// Basic parsing - just validate format
	result["connectionString"] = connStr
	result["protocol"] = "mongodb"
	if strings.HasPrefix(connStr, "mongodb+srv://") {
		result["protocol"] = "mongodb+srv"
	}

	return result, nil
}

// mongoDBBackupSchedule schedules MongoDB backups in the runBackups function
// This is called from runBackups in backup.go
func mongoDBBackupSchedule(config *Config, configPath string, t time.Time) {
	isWeeklyDay := t.Weekday() == time.Sunday
	backupBase := filepath.Join(filepath.Dir(configPath), "backups")

	for _, server := range config.Servers {
		if detectDBType(server.RootConnectionString) != MongoDB {
			continue
		}

		for _, db := range server.Databases {
			if db.Backup == nil || !db.Backup.Enabled {
				continue
			}
			if db.Backup.Schedule == "weekly" && !isWeeklyDay {
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
				pruneMongoDBBackups(dir, db.Database, db.Backup.KeepCount)
			}
		}
	}
}

// pruneMongoDBBackups removes old MongoDB backup files beyond keep_count
func pruneMongoDBBackups(dir, database string, keepCount int) {
	pattern := filepath.Join(dir, database+"_*.archive.gz")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) <= keepCount {
		return
	}

	// Sort by filename (which includes date in YYYY-MM-DD format)
	for i := 0; i < len(files)-keepCount; i++ {
		if err := os.Remove(files[i]); err != nil {
			log.Printf("backup: prune %s: %v", files[i], err)
		} else {
			log.Printf("backup: pruned %s", files[i])
		}
	}
}
