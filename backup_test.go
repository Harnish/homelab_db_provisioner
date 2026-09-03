package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunBackups_SkipsCompletedMigration(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Servers: []DatabaseServer{{
		Name:                 "old-pg",
		RootConnectionString: "postgres://r:p@127.0.0.1:1/postgres",
		Databases: []DatabaseConfig{{
			Database: "app", User: "app", Password: "pw",
			Backup:  &BackupConfig{Enabled: true, Schedule: "daily"},
			Migrate: &MigrateConfig{TargetServer: "new-pg", Completed: true},
		}},
	}}}

	runBackups(cfg, configPath, time.Now())

	if _, err := os.Stat(filepath.Join(dir, "backups", "old-pg", "app")); !os.IsNotExist(err) {
		t.Fatalf("expected no backup dir for tombstoned entry, stat err = %v", err)
	}
}

func TestConnStrWithDB(t *testing.T) {
	got, err := connStrWithDB("postgres://root:pass@host:5432/postgres", "app")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "postgres://root:pass@host:5432/app" {
		t.Errorf("got %q", got)
	}
}

func TestConnStrWithDB_BadURL(t *testing.T) {
	if _, err := connStrWithDB("://not a url", "app"); err == nil {
		t.Fatal("expected error for malformed connection string")
	}
}

func TestRunBackups_NoS3ConfigDoesNotPanic(t *testing.T) {
	config := &Config{Servers: nil, S3: nil}
	// Must not panic or attempt any S3 client construction when S3 is nil.
	runBackups(config, "/tmp/does-not-matter/config.json", time.Now())
}

func TestFindNewestBackup_NoLocalNoS3ReturnsEmpty(t *testing.T) {
	config := &Config{Servers: nil, S3: nil}
	got := findNewestBackup(config, "/tmp/does-not-exist-config-dir/config.json", "myserver", "mydb", PostgreSQL)
	if got != "" {
		t.Errorf("findNewestBackup() = %q, want empty string", got)
	}
}
