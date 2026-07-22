package main

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestApplyK8sPassword_MultipleDatabasesGetDistinctSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	dbs := []DatabaseConfig{
		{Database: "app_db", Password: "config-a"},
		{Database: "analytics_db", Password: "config-b"},
	}

	var resolved []DatabaseConfig
	for _, db := range dbs {
		got, err := applyK8sPassword(context.Background(), "Main PostgreSQL", db)
		if err != nil {
			t.Fatalf("applyK8sPassword(%s) error = %v", db.Database, err)
		}
		resolved = append(resolved, got)
	}

	if resolved[0].Password == resolved[1].Password {
		t.Fatalf("expected distinct passwords per database, both got %q", resolved[0].Password)
	}
	if resolved[0].Password == "config-a" || resolved[1].Password == "config-b" {
		t.Fatal("expected config.json passwords to be overridden, not passed through")
	}
}

func TestConfig_S3ConfigRoundTrip(t *testing.T) {
	raw := `{
		"servers": [],
		"s3": {
			"bucket": "my-backups",
			"region": "us-east-1",
			"endpoint": "https://minio.example.com",
			"prefix": "homelab"
		}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.S3 == nil {
		t.Fatal("expected S3 config to be non-nil")
	}
	if cfg.S3.Bucket != "my-backups" {
		t.Errorf("Bucket = %q, want %q", cfg.S3.Bucket, "my-backups")
	}
	if cfg.S3.Region != "us-east-1" {
		t.Errorf("Region = %q, want %q", cfg.S3.Region, "us-east-1")
	}
	if cfg.S3.Endpoint != "https://minio.example.com" {
		t.Errorf("Endpoint = %q, want %q", cfg.S3.Endpoint, "https://minio.example.com")
	}
	if cfg.S3.Prefix != "homelab" {
		t.Errorf("Prefix = %q, want %q", cfg.S3.Prefix, "homelab")
	}
}

func TestConfig_S3ConfigAbsent(t *testing.T) {
	raw := `{"servers": []}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.S3 != nil {
		t.Fatalf("expected S3 config to be nil when absent, got %+v", cfg.S3)
	}
}
