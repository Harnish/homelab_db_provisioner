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
		{Database: "app_db", K8sSecret: "app-db-credentials"},
		{Database: "analytics_db", K8sSecret: "analytics-db-credentials"},
	}

	var resolved []DatabaseConfig
	for _, db := range dbs {
		got, err := applyK8sPassword(context.Background(), "postgres://root:root@localhost:5432/postgres", db)
		if err != nil {
			t.Fatalf("applyK8sPassword(%s) error = %v", db.Database, err)
		}
		resolved = append(resolved, got)
	}

	if resolved[0].Password == "" || resolved[0].Password == resolved[1].Password {
		t.Fatalf("expected distinct generated passwords per database, got %q and %q", resolved[0].Password, resolved[1].Password)
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

func TestProcessConfig_CompletedMigrationSkipsProvisioning(t *testing.T) {
	cfg := &Config{Servers: []DatabaseServer{{
		Name:                 "old-pg",
		RootConnectionString: "postgres://r:p@127.0.0.1:1/postgres",
		Databases: []DatabaseConfig{{
			Database: "app", User: "app", Password: "pw",
			Migrate: &MigrateConfig{TargetServer: "new-pg", Completed: true, CompletedAt: "2026-09-03T00:00:00Z"},
		}},
	}}}

	if err := processConfig(cfg); err != nil {
		t.Fatalf("processConfig returned error: %v", err)
	}
	m := cfg.Servers[0].Databases[0].Migrate
	if m == nil || !m.Completed || m.Error != "" {
		t.Fatalf("completed migration should be untouched, got %+v", m)
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

func TestValidateDatabaseConfig(t *testing.T) {
	ok := DatabaseConfig{Database: "app_db", User: "app-user.1"}
	cases := []struct {
		name    string
		dbType  DBType
		mutate  func(*DatabaseConfig)
		wantErr bool
	}{
		{"plain", PostgreSQL, func(*DatabaseConfig) {}, false},
		{"digit first", PostgreSQL, func(d *DatabaseConfig) { d.Database = "2fauth" }, false},
		{"empty database", PostgreSQL, func(d *DatabaseConfig) { d.Database = "" }, true},
		{"double quote", PostgreSQL, func(d *DatabaseConfig) { d.Database = `a"; DROP DATABASE x; --` }, true},
		{"backtick", MariaDB, func(d *DatabaseConfig) { d.Database = "a`b" }, true},
		{"path traversal", PostgreSQL, func(d *DatabaseConfig) { d.Database = "../etc" }, true},
		{"slash", PostgreSQL, func(d *DatabaseConfig) { d.Database = "a/b" }, true},
		{"quote in user", MariaDB, func(d *DatabaseConfig) { d.User = "a'b" }, true},
		{"too long", PostgreSQL, func(d *DatabaseConfig) { d.User = strings.Repeat("a", 64) }, true},
		{"pg database privilege", PostgreSQL, func(d *DatabaseConfig) { d.Permissions = []string{"connect", "TEMPORARY"} }, false},
		{"pg table privilege", PostgreSQL, func(d *DatabaseConfig) { d.Permissions = []string{"SELECT"} }, true},
		{"mariadb table privilege", MariaDB, func(d *DatabaseConfig) { d.Permissions = []string{"SELECT", "CREATE VIEW"} }, false},
		{"permission injection", MariaDB, func(d *DatabaseConfig) { d.Permissions = []string{"SELECT ON *.* TO x; --"} }, true},
		{"extension", PostgreSQL, func(d *DatabaseConfig) { d.Extensions = []string{"uuid-ossp", "pg_trgm"} }, false},
		{"extension injection", PostgreSQL, func(d *DatabaseConfig) { d.Extensions = []string{`x"; DROP`} }, true},
	}
	for _, c := range cases {
		db := ok
		c.mutate(&db)
		if err := validateDatabaseConfig(c.dbType, db); (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestQuoting(t *testing.T) {
	if got := quoteIdentifier(`a"b`); got != `"a""b"` {
		t.Errorf("quoteIdentifier: %s", got)
	}
	if got := mysqlIdentifier("a`b"); got != "`a``b`" {
		t.Errorf("mysqlIdentifier: %s", got)
	}
	if got := mysqlLiteral(`it's\`); got != `'it''s\\'` {
		t.Errorf("mysqlLiteral: %s", got)
	}
}

func TestMariaDBDSN(t *testing.T) {
	cases := map[string]string{
		"mariadb://root:pw@db:3306/":                "root:pw@tcp(db:3306)/",
		"mysql://root:p@ss@db:3306/app?parseTime=1": "root:p@ss@tcp(db:3306)/app?parseTime=1",
		"mariadb://root:pw@db/":                     "root:pw@tcp(db:3306)/",
		"mariadb://root:pw@db:3306":                 "root:pw@tcp(db:3306)/",
	}
	for in, want := range cases {
		got, err := mariaDBDSN(in)
		if err != nil || got != want {
			t.Errorf("mariaDBDSN(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := mariaDBDSN("mariadb://db:3306/"); err == nil {
		t.Error("expected error for connection string without credentials")
	}
}

// The example configs are what a new user copies first; they must validate.
func TestExampleConfigsValidate(t *testing.T) {
	for _, path := range []string{"config.json", "config-multi.json"} {
		cfg, err := loadConfigFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, s := range cfg.Servers {
			for _, db := range s.Databases {
				if err := validateDatabaseConfig(detectDBType(s.RootConnectionString), db); err != nil {
					t.Errorf("%s: %s/%s: %v", path, s.Name, db.Database, err)
				}
			}
		}
	}
}
