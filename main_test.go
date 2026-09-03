package main

import (
	"context"
	"encoding/json"
	"strings"
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
		got, err := applyK8sPassword(context.Background(), "Main PostgreSQL", "postgres://root:root@localhost:5432/postgres", db)
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

func TestConfig_MigrateConfigRoundTrip(t *testing.T) {
	raw := `{
		"servers": [
			{
				"name": "old-pg",
				"root_connection_string": "postgres://root:pass@old:5432/postgres",
				"databases": [
					{
						"database": "app",
						"user": "app",
						"password": "pw",
						"migrate": {
							"target_server": "new-pg",
							"confirm_drop": true,
							"completed": true,
							"completed_at": "2026-09-03T12:00:00Z",
							"error": ""
						}
					}
				]
			}
		]
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := cfg.Servers[0].Databases[0].Migrate
	if m == nil {
		t.Fatal("expected Migrate to be non-nil")
	}
	if m.TargetServer != "new-pg" || !m.ConfirmDrop || !m.Completed || m.CompletedAt != "2026-09-03T12:00:00Z" {
		t.Errorf("unexpected Migrate: %+v", m)
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"target_server":"new-pg"`) {
		t.Errorf("marshalled config missing migrate block: %s", out)
	}
}

func TestConfig_MigrateConfigAbsent(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"servers":[{"name":"x","root_connection_string":"postgres://x","databases":[{"database":"d","user":"u","password":"p"}]}]}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Servers[0].Databases[0].Migrate != nil {
		t.Error("expected Migrate to be nil")
	}
	out, _ := json.Marshal(cfg)
	if strings.Contains(string(out), "migrate") {
		t.Errorf("absent migrate should be omitted: %s", out)
	}
}
