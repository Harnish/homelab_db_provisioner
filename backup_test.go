package main

import (
	"os"
	"path/filepath"
	"strings"
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

	runBackups(cfg, configPath, time.Now(), false)

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
	runBackups(config, "/tmp/does-not-matter/config.json", time.Now(), false)
}

func TestFindNewestBackup_NoLocalNoS3ReturnsEmpty(t *testing.T) {
	config := &Config{Servers: nil, S3: nil}
	got := findNewestBackup(config, "/tmp/does-not-exist-config-dir/config.json", "myserver", "mydb", PostgreSQL)
	if got != "" {
		t.Errorf("findNewestBackup() = %q, want empty string", got)
	}
}

func TestMongoToolURI(t *testing.T) {
	cases := map[string]string{
		"mongodb://admin:pw@m:27017/admin":                    "mongodb://admin:pw@m:27017/?authSource=admin",
		"mongodb://admin:pw@m1:27017,m2:27017/admin?tls=true": "mongodb://admin:pw@m1:27017,m2:27017/?tls=true&authSource=admin",
		"mongodb://admin:pw@m:27017/x?authSource=admin":       "mongodb://admin:pw@m:27017/?authSource=admin",
		"mongodb://admin:pw@m:27017/":                         "mongodb://admin:pw@m:27017/",
		"mongodb://admin:pw@m:27017":                          "mongodb://admin:pw@m:27017",
	}
	for in, want := range cases {
		if got := mongoToolURI(in); got != want {
			t.Errorf("mongoToolURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPgCommand_PasswordNotInArgs(t *testing.T) {
	cmd := pgCommand("pg_dump", "postgres://postgres:s3cr%40t@db:5432/app?sslmode=disable")
	if strings.Contains(strings.Join(cmd.Args, " "), "s3cr") {
		t.Errorf("password in argv: %v", cmd.Args)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != "postgres://postgres@db:5432/app?sslmode=disable" {
		t.Errorf("connection arg = %q", got)
	}
	found := false
	for _, e := range cmd.Env {
		found = found || e == "PGPASSWORD=s3cr@t"
	}
	if !found {
		t.Error("PGPASSWORD not set to the decoded password")
	}
}

func TestBackupDue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	sunday := time.Date(2026, 11, 15, 9, 0, 0, 0, time.Local)
	monday := sunday.AddDate(0, 0, 1)
	daily := DatabaseConfig{Database: "app", Backup: &BackupConfig{Enabled: true}}
	weekly := DatabaseConfig{Database: "app", Backup: &BackupConfig{Enabled: true, Schedule: "weekly"}}
	touch := func(day time.Time) {
		dir := filepath.Join(filepath.Dir(configPath), "backups", "srv", "app")
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0750)
		os.WriteFile(filepath.Join(dir, "app_"+day.Format("2006-01-02")+".sql.gz"), nil, 0600)
	}

	// Scheduled runs: daily always, weekly on Sundays only.
	if !backupDue(configPath, "srv", daily, monday, false) || backupDue(configPath, "srv", weekly, monday, false) || !backupDue(configPath, "srv", weekly, sunday, false) {
		t.Error("scheduled run picked the wrong backups")
	}
	// Catch-up: no backup yet.
	if !backupDue(configPath, "srv", daily, monday, true) {
		t.Error("catch-up should take a database with no backup")
	}
	// Catch-up: today's backup exists.
	touch(monday)
	if backupDue(configPath, "srv", daily, monday, true) || backupDue(configPath, "srv", weekly, monday, true) {
		t.Error("catch-up repeated a backup taken today")
	}
	// Catch-up: midnight was missed (newest is yesterday's).
	touch(sunday)
	if !backupDue(configPath, "srv", daily, monday, true) {
		t.Error("catch-up should take a daily backup missed at midnight")
	}
	if backupDue(configPath, "srv", weekly, monday, true) {
		t.Error("catch-up took a weekly backup that is one day old")
	}
	touch(sunday.AddDate(0, 0, -7))
	if !backupDue(configPath, "srv", weekly, sunday, true) {
		t.Error("catch-up should take a weekly backup that is a week old")
	}
	if off := (DatabaseConfig{Database: "app", Backup: &BackupConfig{}}); backupDue(configPath, "srv", off, monday, true) {
		t.Error("disabled backup taken")
	}
}
