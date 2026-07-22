package main

import (
	"testing"
	"time"
)

func TestRunBackups_NoS3ConfigDoesNotPanic(t *testing.T) {
	config := &Config{Servers: nil, S3: nil}
	// Must not panic or attempt any S3 client construction when S3 is nil.
	runBackups(config, "/tmp/does-not-matter/config.json", time.Now())
}
