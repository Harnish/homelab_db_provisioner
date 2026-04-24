package main

import (
	"compress/gzip"
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
)

func startBackupScheduler(configPath string) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		time.Sleep(time.Until(next))

		t := time.Now()
		configMu.RLock()
		config, err := loadConfig()
		configMu.RUnlock()
		if err != nil {
			log.Printf("backup: load config: %v", err)
			continue
		}
		runBackups(config, configPath, t)
	}
}

func runBackups(config *Config, configPath string, t time.Time) {
	isWeeklyDay := t.Weekday() == time.Sunday
	backupBase := filepath.Join(filepath.Dir(configPath), "backups")

	for _, server := range config.Servers {
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

			filename := filepath.Join(dir, fmt.Sprintf("%s_%s.sql.gz", db.Database, t.Format("2006-01-02")))

			var err error
			if detectDBType(server.RootConnectionString) == PostgreSQL {
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
				pruneBackups(dir, db.Database, db.Backup.KeepCount)
			}
		}
	}
}

func backupPostgreSQL(rootConnStr, database, destFile string) error {
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/" + database
	connStr := u.String()

	f, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	gz := gzip.NewWriter(f)
	cmd := exec.Command("pg_dump", "--no-password", connStr)
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

	f, err := os.Create(destFile)
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

	host, port, err = net.SplitHostPort(hostPort)
	return
}

func pruneBackups(dir, database string, keepCount int) {
	pattern := filepath.Join(dir, database+"_*.sql.gz")
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

func restoreDatabase(server DatabaseServer, db DatabaseConfig, configPath string) {
	backupFile := findNewestBackup(configPath, server.Name, db.Database)
	if backupFile == "" {
		log.Printf("restore: no backup found for %s/%s, skipping", server.Name, db.Database)
		return
	}

	log.Printf("restore: restoring %s/%s from %s", server.Name, db.Database, backupFile)

	var err error
	if detectDBType(server.RootConnectionString) == PostgreSQL {
		err = restorePostgreSQL(server.RootConnectionString, db.Database, backupFile)
	} else {
		err = restoreMariaDB(server.RootConnectionString, db.Database, backupFile)
	}

	if err != nil {
		log.Printf("restore: %s/%s: %v", server.Name, db.Database, err)
	} else {
		log.Printf("restore: %s/%s complete", server.Name, db.Database)
	}
}

func findNewestBackup(configPath, serverName, database string) string {
	dir := filepath.Join(filepath.Dir(configPath), "backups", slugify(serverName), database)
	pattern := filepath.Join(dir, database+"_*.sql.gz")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	return files[len(files)-1]
}

func restorePostgreSQL(rootConnStr, database, backupFile string) error {
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/" + database
	connStr := u.String()

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

	cmd := exec.Command("psql", "--no-password", connStr)
	cmd.Stdin = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, stderr.String())
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
