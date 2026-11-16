//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// A password with every character that has bitten the quoting code.
const nastyPassword = `p'w"\x!@#$%^&*` + "`"

func TestProvisionPostgreSQL_Integration(t *testing.T) {
	root := os.Getenv("MIGRATE_SOURCE_CONN")
	if root == "" {
		t.Skip("MIGRATE_SOURCE_CONN not set")
	}
	db, err := sql.Open("postgres", root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanup := func() {
		db.Exec(`DROP DATABASE IF EXISTS prov_it WITH (FORCE)`)
		db.Exec(`DROP ROLE IF EXISTS prov_it`)
	}
	cleanup()
	defer cleanup()

	cfg := DatabaseConfig{Database: "prov_it", User: "prov_it", Password: nastyPassword, Permissions: []string{"CONNECT", "TEMPORARY"}}

	// Dry run must not create anything or log the password.
	var logs bytes.Buffer
	log.SetOutput(&logs)
	_, err = provisionPostgreSQL(db, root, cfg, true)
	log.SetOutput(os.Stderr)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if strings.Contains(logs.String(), nastyPassword) || !strings.Contains(logs.String(), redactedLiteral) {
		t.Errorf("dry-run log should redact the password, got:\n%s", logs.String())
	}
	if exists, _ := checkPostgreSQLDatabaseExists(context.Background(), db, "prov_it"); exists {
		t.Fatal("dry run created the database")
	}

	for run, wantCreated := range []bool{true, false} {
		created, err := provisionPostgreSQL(db, root, cfg, false)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if created != wantCreated {
			t.Errorf("run %d: created=%v, want %v", run, created, wantCreated)
		}
	}

	userConn, err := buildConnectString(root, cfg, nastyPassword)
	if err != nil {
		t.Fatal(err)
	}
	udb, err := sql.Open("postgres", userConn)
	if err != nil {
		t.Fatal(err)
	}
	defer udb.Close()
	if _, err := udb.Exec(`CREATE TABLE t (id int)`); err != nil {
		t.Fatalf("provisioned user can't use its database: %v", err)
	}
}

func TestProvisionMariaDB_Integration(t *testing.T) {
	root := os.Getenv("TEST_MARIADB_CONN")
	if root == "" {
		t.Skip("TEST_MARIADB_CONN not set")
	}
	dsn, err := mariaDBDSN(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanup := func() {
		db.Exec("DROP DATABASE IF EXISTS prov_it")
		db.Exec("DROP USER IF EXISTS 'prov_it'@'%'")
	}
	cleanup()
	defer cleanup()

	cfg := DatabaseConfig{Database: "prov_it", User: "prov_it", Password: nastyPassword, Permissions: []string{"SELECT", "INSERT", "CREATE"}}
	for run, wantCreated := range []bool{true, false} {
		created, err := provisionMariaDB(db, cfg, false)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if created != wantCreated {
			t.Errorf("run %d: created=%v, want %v", run, created, wantCreated)
		}
	}

	host, port, _, _, _ := parseMariaDBConn(root)
	uc := mysql.NewConfig()
	uc.User, uc.Passwd, uc.Net, uc.Addr, uc.DBName = "prov_it", nastyPassword, "tcp", host+":"+port, "prov_it"
	udb, err := sql.Open("mysql", uc.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer udb.Close()
	if _, err := udb.Exec("CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("provisioned user can't use its database: %v", err)
	}
}

func TestProvisionMongoDB_Integration(t *testing.T) {
	root := os.Getenv("TEST_MONGO_CONN")
	if root == "" {
		t.Skip("TEST_MONGO_CONN not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(root))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	cleanup := func() {
		client.Database("prov_it").Drop(ctx)
		client.Database("admin").RunCommand(ctx, map[string]string{"dropUser": "prov_it"})
	}
	cleanup()
	defer cleanup()

	cfg := DatabaseConfig{Database: "prov_it", User: "prov_it", Password: nastyPassword}
	for run, wantCreated := range []bool{true, false} {
		created, err := provisionMongoDB(root, cfg, false)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if created != wantCreated {
			t.Errorf("run %d: created=%v, want %v", run, created, wantCreated)
		}
	}

	userClient, err := mongo.Connect(ctx, options.Client().ApplyURI(root).SetAuth(options.Credential{
		Username: "prov_it", Password: nastyPassword, AuthSource: "admin",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer userClient.Disconnect(ctx)
	if _, err := userClient.Database("prov_it").Collection("t").InsertOne(ctx, map[string]int{"n": 1}); err != nil {
		t.Fatalf("provisioned user can't use its database: %v", err)
	}
}

func TestBackupRestorePostgreSQL_Integration(t *testing.T) {
	root := os.Getenv("MIGRATE_SOURCE_CONN")
	if root == "" {
		t.Skip("MIGRATE_SOURCE_CONN not set")
	}
	db, err := sql.Open("postgres", root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanup := func() {
		db.Exec(`DROP DATABASE IF EXISTS bak_it WITH (FORCE)`)
		db.Exec(`DROP ROLE IF EXISTS bak_it`)
	}
	cleanup()
	defer cleanup()
	if _, err := provisionPostgreSQL(db, root, DatabaseConfig{Database: "bak_it", User: "bak_it", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	conn, _ := connStrWithDB(root, "bak_it")
	bdb, err := sql.Open("postgres", conn)
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	if _, err := bdb.Exec(`CREATE TABLE t AS SELECT generate_series(1, 5) AS id`); err != nil {
		t.Fatal(err)
	}

	dump := filepath.Join(t.TempDir(), "bak_it_2026-01-01.sql.gz")
	if err := backupPostgreSQL(root, "bak_it", dump); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, _ := os.Stat(dump); fi.Mode().Perm() != 0600 {
		t.Errorf("dump mode = %v, want 0600", fi.Mode().Perm())
	}

	if _, err := bdb.Exec(`DROP TABLE t`); err != nil {
		t.Fatal(err)
	}
	if err := restorePostgreSQL(root, "bak_it", dump); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var n int
	if err := bdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("after restore: n=%d err=%v", n, err)
	}
}

func TestBackupRestoreMongoDB_Integration(t *testing.T) {
	root := os.Getenv("TEST_MONGO_CONN")
	if root == "" {
		t.Skip("TEST_MONGO_CONN not set")
	}
	if _, err := exec.LookPath("mongodump"); err != nil {
		t.Skip("mongodump not installed")
	}
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(root))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	coll := client.Database("bak_it").Collection("t")
	defer client.Database("bak_it").Drop(ctx)
	if _, err := coll.InsertOne(ctx, map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}

	dump := filepath.Join(t.TempDir(), "bak_it_2026-01-01.archive.gz")
	if err := backupMongoDB(root, "bak_it", dump); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := coll.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restoreMongoDB(root, "bak_it", dump); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n, err := coll.CountDocuments(ctx, map[string]int{}); err != nil || n != 1 {
		t.Fatalf("after restore: n=%d err=%v", n, err)
	}
}
