package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func startBackupScheduler(configPath string) {
	// Backups only fire at midnight, so one missed while the provisioner was
	// down would otherwise wait a day (or a week). Catch up once at startup,
	// after the first provisioning pass has had time to create new databases.
	time.Sleep(2 * time.Minute)
	catchUp := true
	for {
		t := time.Now()
		configMu.RLock()
		config, err := loadConfig()
		configMu.RUnlock()
		if err != nil {
			log.Printf("backup: load config: %v", err)
		} else {
			runBackups(config, configPath, t, catchUp)
		}
		catchUp = false

		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		time.Sleep(time.Until(next))
	}
}

// backupDue reports whether db should be backed up in this run. A scheduled
// run takes every daily backup, and weekly ones on Sundays. A catch-up run
// takes only those whose newest local backup is older than the schedule.
func backupDue(configPath, serverName string, db DatabaseConfig, t time.Time, catchUp bool) bool {
	if db.Migrate != nil && db.Migrate.Completed {
		return false
	}
	if db.Backup == nil || !db.Backup.Enabled {
		return false
	}
	weekly := db.Backup.Schedule == "weekly"
	if !catchUp {
		return !weekly || t.Weekday() == time.Sunday
	}
	today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	days, ok := newestBackupDays(configPath, serverName, db.Database, today)
	if !ok {
		return true
	}
	if weekly {
		return days >= 7
	}
	return days >= 1
}

func runBackups(config *Config, configPath string, t time.Time, catchUp bool) {
	backupBase := filepath.Join(filepath.Dir(configPath), "backups")

	var s3Client *s3.Client
	if config.S3 != nil {
		client, err := newS3Client(context.Background(), config.S3)
		if err != nil {
			log.Printf("backup: s3 client: %v", err)
		} else {
			s3Client = client
		}
	}

	for _, server := range config.Servers {
		dbType := detectDBType(server.RootConnectionString)

		// Handle MongoDB separately
		if dbType == MongoDB {
			backupMongoServer(config, server, configPath, t, catchUp, s3Client)
			continue
		}

		for _, db := range server.Databases {
			if !backupDue(configPath, server.Name, db, t, catchUp) {
				continue
			}

			dir := filepath.Join(backupBase, slugify(server.Name), db.Database)
			if err := os.MkdirAll(dir, 0750); err != nil {
				log.Printf("backup: mkdir %s: %v", dir, err)
				continue
			}

			filename := filepath.Join(dir, fmt.Sprintf("%s_%s.sql.gz", db.Database, t.Format("2006-01-02")))

			var err error
			if dbType == PostgreSQL {
				err = backupPostgreSQL(server.RootConnectionString, db.Database, filename)
			} else {
				err = backupMariaDB(server.RootConnectionString, db.Database, filename)
			}

			if err != nil {
				log.Printf("backup: %s/%s: %v", server.Name, db.Database, err)
				os.Remove(filename)
				continue
			}

			log.Printf("backup: created %s", filename)
			if db.Backup.KeepCount > 0 {
				pruneBackups(dir, db.Database, ".sql.gz", db.Backup.KeepCount)
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
}

// createPrivate creates a dump file readable only by the provisioner's user:
// dumps hold everything the database does.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
}

// pgCommand builds a pg_dump/psql command for connStr with the password
// passed in PGPASSWORD rather than on the command line, where any local user
// could read it from the process list.
func pgCommand(name, connStr string, args ...string) *exec.Cmd {
	env := os.Environ()
	if u, err := url.Parse(connStr); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			env = append(env, "PGPASSWORD="+pw)
			u.User = url.User(u.User.Username())
			connStr = u.String()
		}
	}
	cmd := exec.Command(name, append(append([]string{"--no-password"}, args...), connStr)...)
	cmd.Env = env
	return cmd
}

// connStrWithDB rewrites a root connection URL to point at a specific database.
func connStrWithDB(rootConnStr, database string) (string, error) {
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return "", fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/" + database
	return u.String(), nil
}

func backupPostgreSQL(rootConnStr, database, destFile string) error {
	connStr, err := connStrWithDB(rootConnStr, database)
	if err != nil {
		return err
	}

	f, err := createPrivate(destFile)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	gz := gzip.NewWriter(f)
	cmd := pgCommand("pg_dump", connStr)
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		gz.Close()
		f.Close()
		return fmt.Errorf("pg_dump: %w: %s", err, stderr.String())
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return fmt.Errorf("gzip close: %w", err)
	}
	return f.Close()
}

func backupMariaDB(rootConnStr, database, destFile string) error {
	host, port, user, pass, err := parseMariaDBConn(rootConnStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}

	f, err := createPrivate(destFile)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	gz := gzip.NewWriter(f)
	cmd := exec.Command("mysqldump", "-h", host, "-P", port, "-u", user, database)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		gz.Close()
		f.Close()
		return fmt.Errorf("mysqldump: %w: %s", err, stderr.String())
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return fmt.Errorf("gzip close: %w", err)
	}
	return f.Close()
}

func parseMariaDBConn(connStr string) (host, port, user, pass string, err error) {
	s := strings.TrimPrefix(connStr, "mariadb://")
	s = strings.TrimPrefix(s, "mysql://")
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	atIdx := strings.LastIndex(s, "@")
	if atIdx < 0 {
		err = fmt.Errorf("missing @ in connection string")
		return
	}
	creds := s[:atIdx]
	hostPort := s[atIdx+1:]

	colonIdx := strings.Index(creds, ":")
	if colonIdx < 0 {
		err = fmt.Errorf("missing : in credentials")
		return
	}
	user = creds[:colonIdx]
	pass = creds[colonIdx+1:]

	if host, port, err = net.SplitHostPort(hostPort); err != nil {
		// No port given: the server default.
		host, port, err = hostPort, "3306", nil
	}
	return
}

// mariaDBDSN converts mariadb://user:pass@host:port/db?params into the
// go-sql-driver DSN user:pass@tcp(host:port)/db?params.
func mariaDBDSN(connStr string) (string, error) {
	host, port, user, pass, err := parseMariaDBConn(connStr)
	if err != nil {
		return "", err
	}
	s := strings.TrimPrefix(strings.TrimPrefix(connStr, "mariadb://"), "mysql://")
	_, rest, _ := strings.Cut(s, "/")
	return user + ":" + pass + "@tcp(" + net.JoinHostPort(host, port) + ")/" + rest, nil
}

func pruneBackups(dir, database, suffix string, keepCount int) {
	pattern := filepath.Join(dir, database+"_*"+suffix)
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) <= keepCount {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-keepCount] {
		if err := os.Remove(f); err != nil {
			log.Printf("backup: prune %s: %v", f, err)
		} else {
			log.Printf("backup: pruned %s", f)
		}
	}
}

func restoreDatabase(config *Config, server DatabaseServer, db DatabaseConfig, configPath string) {
	dbType := detectDBType(server.RootConnectionString)
	backupFile := findNewestBackup(config, configPath, server.Name, db.Database, dbType)
	if backupFile == "" {
		log.Printf("restore: no backup found for %s/%s, skipping", server.Name, db.Database)
		return
	}

	log.Printf("restore: restoring %s/%s from %s", server.Name, db.Database, backupFile)

	var err error
	if dbType == PostgreSQL {
		err = restorePostgreSQL(server.RootConnectionString, db.Database, backupFile)
	} else if dbType == MongoDB {
		err = restoreMongoDB(server.RootConnectionString, db.Database, backupFile)
	} else {
		err = restoreMariaDB(server.RootConnectionString, db.Database, backupFile)
	}

	if err != nil {
		log.Printf("restore: %s/%s: %v", server.Name, db.Database, err)
	} else {
		log.Printf("restore: %s/%s complete", server.Name, db.Database)
	}
}

func findNewestBackup(config *Config, configPath, serverName, database string, dbType DBType) string {
	dir := filepath.Join(filepath.Dir(configPath), "backups", slugify(serverName), database)

	var pattern string
	if dbType == MongoDB {
		pattern = filepath.Join(dir, database+"_*.archive.gz")
	} else {
		pattern = filepath.Join(dir, database+"_*.sql.gz")
	}

	files, err := filepath.Glob(pattern)
	if err == nil && len(files) > 0 {
		sort.Strings(files)
		return files[len(files)-1]
	}

	if config == nil || config.S3 == nil {
		return ""
	}

	ctx := context.Background()
	client, err := newS3Client(ctx, config.S3)
	if err != nil {
		log.Printf("restore: s3 client: %v", err)
		return ""
	}

	localPath, err := downloadNewestFromS3(ctx, client, config.S3, slugify(serverName), database, dir)
	if err != nil {
		log.Printf("restore: s3 download %s/%s: %v", serverName, database, err)
		return ""
	}
	return localPath
}

func restorePostgreSQL(rootConnStr, database, backupFile string) error {
	connStr, err := connStrWithDB(rootConnStr, database)
	if err != nil {
		return err
	}

	f, err := os.Open(backupFile)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	cmd := pgCommand("psql", connStr)
	cmd.Stdin = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, stderr.String())
	}
	// psql exits 0 after failed statements. Aborting on the first one would
	// reject dumps that restore fine apart from a harmless error (a setting
	// the target version lacks), so finish, but don't report a clean restore.
	if strings.Contains(stderr.String(), "ERROR:") {
		log.Printf("restore: psql reported errors, %s may be incomplete: %s", database, stderr.String())
	}
	return nil
}

func restoreMariaDB(rootConnStr, database, backupFile string) error {
	host, port, user, pass, err := parseMariaDBConn(rootConnStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}

	f, err := os.Open(backupFile)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	cmd := exec.Command("mysql", "-h", host, "-P", port, "-u", user, database)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	cmd.Stdin = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysql: %w: %s", err, stderr.String())
	}
	return nil
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}
