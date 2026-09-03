package main

import (
	"testing"
	"time"
)

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
