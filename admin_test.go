package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testConfigJSON = `{
  "servers": [
    {
      "name": "Test Server",
      "root_connection_string": "postgres://root:pass@localhost/postgres",
      "databases": [
        {"database": "mydb", "user": "myuser", "password": "mypass"}
      ]
    }
  ]
}`

const testConfigWithBackupJSON = `{
  "servers": [
    {
      "name": "Test Server",
      "root_connection_string": "postgres://root:pass@localhost/postgres",
      "databases": [
        {
          "database": "mydb",
          "user": "myuser",
          "password": "mypass",
          "backup": {
            "enabled": true,
            "schedule": "daily",
            "keep_count": 7,
            "restore_on_create": false
          }
        }
      ]
    }
  ]
}`

const testConfigK8sJSON = `{
  "servers": [
    {
      "name": "Test Server",
      "root_connection_string": "postgres://root:pass@localhost/postgres",
      "databases": [
        {"database": "mydb", "user": "myuser", "k8s_secret": "mydb-credentials"}
      ]
    }
  ]
}`

func makeTestConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestBasicAuth_Unauthorized(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("expected WWW-Authenticate header")
	}
}

func TestBasicAuth_WrongPassword(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrongpassword")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestFavicon_NoAuthChallenge(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("favicon should not send an auth challenge")
	}
}

func TestBasicAuth_Authorized(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestIndex_ShowsDatabasesAndForms(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{"Test Server", "mydb", "myuser", "Add Database", `action="/update-password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
}

func TestIndex_ShowsFlashMessage(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/?msg=Password+updated", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "Password updated") {
		t.Error("expected flash message in body")
	}
}

func TestIndex_UnknownPathReturns404(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdatePassword_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index": {"0"},
		"db_index":     {"0"},
		"new_password": {"newpassword123"},
	}
	req := httptest.NewRequest("POST", "/update-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Databases[0].Password != "newpassword123" {
		t.Errorf("expected password updated in config, got %q", cfg.Servers[0].Databases[0].Password)
	}
}

func TestUpdatePassword_InvalidServerIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{
		"server_index": {"99"},
		"db_index":     {"0"},
		"new_password": {"newpassword"},
	}
	req := httptest.NewRequest("POST", "/update-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestUpdatePassword_InvalidDBIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{
		"server_index": {"0"},
		"db_index":     {"99"},
		"new_password": {"newpassword"},
	}
	req := httptest.NewRequest("POST", "/update-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIndex_ShowsMigrateFormForPostgres(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[
		{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]},
		{"name":"new-pg","root_connection_string":"postgres://r:p@new/postgres","databases":[]}
	]}`
	h := newAdminHandler(makeTestConfig(t, cfg))
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `action="/migrate-database"`) {
		t.Error("expected migrate form")
	}
	if !strings.Contains(body, `value="new-pg"`) {
		t.Error("expected new-pg as a migrate target option")
	}
}

func TestIndex_ShowsMigrateStatusBadge(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw","migrate":{"target_server":"new-pg","completed":true,"completed_at":"2026-09-03T12:00:00Z"}}]}]}`
	h := newAdminHandler(makeTestConfig(t, cfg))
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "migrated to <strong>new-pg</strong>") {
		t.Errorf("expected completed badge, body:\n%s", body)
	}
	if !strings.Contains(body, "Clear") {
		t.Error("completed row should offer a Clear button")
	}
}

func TestMigrateDatabase_SetsBlock(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[
		{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]},
		{"name":"new-pg","root_connection_string":"postgres://r:p@new/postgres","databases":[]}
	]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":     {"0"},
		"db_index":         {"0"},
		"target_server":    {"new-pg"},
		"confirm_drop":     {"on"},
		"confirm_database": {"app"},
	}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	m := out.Servers[0].Databases[0].Migrate
	if m == nil || m.TargetServer != "new-pg" || !m.ConfirmDrop {
		t.Fatalf("unexpected migrate block: %+v", m)
	}
}

func TestMigrateDatabase_EmptyTargetClears(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw","migrate":{"target_server":"new-pg","error":"boom"}}]}]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}, "target_server": {""}}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	if out.Servers[0].Databases[0].Migrate != nil {
		t.Fatalf("expected migrate cleared, got %+v", out.Servers[0].Databases[0].Migrate)
	}
}

func TestMigrateDatabase_RejectsSameServer(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]}]}`
	path := makeTestConfig(t, cfg)
	h := newAdminHandler(path)

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}, "target_server": {"old-pg"}}
	req := httptest.NewRequest("POST", "/migrate-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "Error") {
		t.Fatalf("expected error flash, got %q", w.Header().Get("Location"))
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	if out.Servers[0].Databases[0].Migrate != nil {
		t.Fatal("migrate block should not have been written")
	}
}

func TestMigrateDatabase_WrongMethod(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)
	req := httptest.NewRequest("GET", "/migrate-database", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestAddDatabase_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index": {"0"},
		"database":     {"newdb"},
		"user":         {"newuser"},
		"password":     {"newpass"},
		"permissions":  {"SELECT, INSERT"},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers[0].Databases) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(cfg.Servers[0].Databases))
	}
	added := cfg.Servers[0].Databases[1]
	if added.Database != "newdb" || added.User != "newuser" || added.Password != "newpass" {
		t.Errorf("unexpected database entry: %+v", added)
	}
	if len(added.Permissions) != 2 || added.Permissions[0] != "SELECT" || added.Permissions[1] != "INSERT" {
		t.Errorf("unexpected permissions: %v", added.Permissions)
	}
}

func TestAddDatabase_RequiresConnectStringChecked(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":            {"0"},
		"database":                {"newdb"},
		"user":                    {"newuser"},
		"password":                {"newpass"},
		"requires_connect_string": {"on"},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	added := cfg.Servers[0].Databases[len(cfg.Servers[0].Databases)-1]
	if !added.RequiresConnectString {
		t.Errorf("expected RequiresConnectString true, got false")
	}
}

func TestAddDatabase_RequiresConnectStringDefaultsFalse(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index": {"0"},
		"database":     {"newdb"},
		"user":         {"newuser"},
		"password":     {"newpass"},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	added := cfg.Servers[0].Databases[len(cfg.Servers[0].Databases)-1]
	if added.RequiresConnectString {
		t.Errorf("expected RequiresConnectString false when checkbox omitted, got true")
	}
}

func TestAddDatabase_EmptyPermissions(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index": {"0"},
		"database":     {"newdb"},
		"user":         {"newuser"},
		"password":     {"newpass"},
		"permissions":  {""},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	added := cfg.Servers[0].Databases[1]
	if len(added.Permissions) != 0 {
		t.Errorf("expected empty permissions, got %v", added.Permissions)
	}
}

func TestAddDatabase_InvalidServerIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{
		"server_index": {"99"},
		"database":     {"newdb"},
		"user":         {"newuser"},
		"password":     {"newpass"},
		"permissions":  {""},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"name":                   {"New Server"},
		"root_connection_string": {"postgres://root:pass@newhost/postgres"},
	}
	req := httptest.NewRequest("POST", "/add-server", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.Servers))
	}
	added := cfg.Servers[1]
	if added.Name != "New Server" || added.RootConnectionString != "postgres://root:pass@newhost/postgres" {
		t.Errorf("unexpected server entry: %+v", added)
	}
	if added.DryRun {
		t.Error("expected dry_run false by default")
	}
	if len(added.Databases) != 0 {
		t.Errorf("expected 0 databases on new server, got %d", len(added.Databases))
	}
}

func TestAddServer_DryRunChecked(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"name":                   {"New Server"},
		"root_connection_string": {"postgres://root:pass@newhost/postgres"},
		"dry_run":                {"on"},
	}
	req := httptest.NewRequest("POST", "/add-server", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Servers[1].DryRun {
		t.Error("expected dry_run true")
	}
}

func TestAddServer_MissingName(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{
		"name":                   {""},
		"root_connection_string": {"postgres://root:pass@newhost/postgres"},
	}
	req := httptest.NewRequest("POST", "/add-server", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddServer_MissingConnectionString(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{
		"name":                   {"New Server"},
		"root_connection_string": {""},
	}
	req := httptest.NewRequest("POST", "/add-server", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddServer_WrongMethod(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/add-server", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestIndex_ShowsK8sSecretWhenOptedIn(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigK8sJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	wantName := "mydb-credentials"
	for _, want := range []string{
		"Rotate", "Kubernetes Secret",
		"name: " + wantName + "\n      key: password",
		"kubectl get secret " + wantName + " -n default",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
	for _, unwanted := range []string{`action="/update-password"`, `action="/move-to-secret"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("did not expect %s for a database already in a Secret", unwanted)
		}
	}
}

func TestIndex_OffersMoveForConfigPasswordInK8sMode(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{`action="/update-password"`, `action="/move-to-secret"`, `name="secret_name" value="mydb-credentials"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s for a database still on its config password", want)
		}
	}
}

func TestMoveToSecret_KeepsPasswordAndClearsConfig(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	w := postAdminForm(t, newAdminHandler(path), "/move-to-secret", url.Values{
		"server_index": {"0"}, "db_index": {"0"}, "database": {"mydb"},
	})
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || strings.Contains(loc, "Error") {
		t.Fatalf("expected success redirect, got %d %q", w.Code, loc)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "mydb-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != "mypass" {
		t.Errorf("secret password = %q, want the existing config password", secret.Data["password"])
	}

	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	db := out.Servers[0].Databases[0]
	if db.K8sSecret != "mydb-credentials" || db.Password != "" {
		t.Errorf("expected k8s_secret stored and password cleared, got %+v", db)
	}
}

func TestMoveToSecret_RejectsNameInUse(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[{"name":"pg","root_connection_string":"postgres://r:p@h/postgres","databases":[
		{"database":"a","user":"a","k8s_secret":"shared-credentials"},
		{"database":"b","user":"b","password":"pw"}
	]}]}`
	path := makeTestConfig(t, cfg)

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	w := postAdminForm(t, newAdminHandler(path), "/move-to-secret", url.Values{
		"server_index": {"0"}, "db_index": {"1"}, "database": {"b"}, "secret_name": {"shared-credentials"},
	})
	if !strings.Contains(w.Header().Get("Location"), "Error") {
		t.Fatalf("expected name-in-use error, got %q", w.Header().Get("Location"))
	}
	if n := len(client.Actions()); n != 0 {
		t.Errorf("expected no Kubernetes calls, got %v", client.Actions())
	}
}

func TestMoveToSecret_DisabledWhenManagerNil(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	secretsManager = nil
	w := postAdminForm(t, newAdminHandler(makeTestConfig(t, testConfigJSON)), "/move-to-secret", url.Values{"server_index": {"0"}, "db_index": {"0"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddDatabase_BlankPasswordIsGenerated(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)

	w := postAdminForm(t, newAdminHandler(path), "/add-database", url.Values{
		"server_index": {"0"}, "database": {"newdb"}, "user": {"newuser"}, "password": {""},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	pw := out.Servers[0].Databases[1].Password
	if len(pw) != 20 {
		t.Fatalf("expected a generated 20-char password, got %q", pw)
	}
	if loc, _ := url.QueryUnescape(w.Header().Get("Location")); strings.Contains(loc, pw) {
		t.Fatalf("redirect leaks the generated password: %q", loc)
	}
}

func TestAddDatabase_K8sModeStartsInSecret(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	w := postAdminForm(t, newAdminHandler(path), "/add-database", url.Values{
		"server_index": {"0"}, "database": {"newdb"}, "user": {"newuser"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	added := out.Servers[0].Databases[1]
	if added.K8sSecret != "newdb-credentials" || added.Password != "" {
		t.Errorf("expected new database in Secret newdb-credentials with no config password, got %+v", added)
	}
}

func TestIndex_ShowsConnectionStringMarkerWhenRequired(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	configJSON := `{
	  "servers": [
	    {
	      "name": "Test Server",
	      "root_connection_string": "postgres://root:pass@localhost/postgres",
	      "databases": [
	        {"database": "mydb", "user": "myuser", "requires_connect_string": true, "k8s_secret": "mydb-credentials"}
	      ]
	    }
	  ]
	}`
	h := newAdminHandler(makeTestConfig(t, configJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "+ connection_string") {
		t.Error("expected connection_string marker in body when RequiresConnectString is true")
	}
	if !strings.Contains(body, "key: connection_string") {
		t.Error("expected connection_string secretKeyRef in the env snippet")
	}
}

func TestIndex_HidesConnectionStringMarkerByDefault(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "+ connection_string") {
		t.Error("did not expect connection_string marker when RequiresConnectString is false")
	}
}

func TestIndex_HidesK8sSecretColumnWhenDisabled(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))
	secretsManager = nil

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Kubernetes Secret") {
		t.Error("did not expect Kubernetes Secret column when disabled")
	}
	if !strings.Contains(body, `action="/update-password"`) {
		t.Error("expected manual password form when k8s secrets disabled")
	}
}

func TestRotateSecret_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigK8sJSON))

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || strings.Contains(loc, "Error") {
		t.Fatalf("expected success redirect, got %d %q", w.Code, loc)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "mydb-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if len(secret.Data["password"]) == 0 {
		t.Error("expected password to be set on the secret")
	}
}

// TestRotateSecret_ReturnsQuicklyEvenWithUnreachableServer verifies that
// handleRotateSecret's HTTP response does not block on reprovisioning the
// database after rotating the Secret. processConfig is dispatched in a
// background goroutine specifically because connectWithRetry can take up
// to ~50s worst case against an unreachable host; the handler must
// redirect immediately regardless.
func TestRotateSecret_ReturnsQuicklyEvenWithUnreachableServer(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")

	const unreachableConfigJSON = `{
	  "servers": [
	    {
	      "name": "Unreachable Server",
	      "root_connection_string": "postgres://root:pass@10.255.255.1:5999/postgres",
	      "databases": [
	        {"database": "mydb", "user": "myuser", "k8s_secret": "mydb-credentials"}
	      ]
	    }
	  ]
	}`
	path := makeTestConfig(t, unreachableConfigJSON)
	h := newAdminHandler(path)

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("handler took %v; expected it to return quickly without waiting for reprovisioning", elapsed)
	}
}

func TestRotateSecret_DisabledWhenManagerNil(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))
	secretsManager = nil

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestRotateSecret_InvalidDBIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"99"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestUpdateBackup_EnablesOnPreviouslyNilBackup(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":             {"0"},
		"db_index":                 {"0"},
		"backup_enabled":           {"on"},
		"backup_schedule":          {"weekly"},
		"backup_keep_count":        {"5"},
		"backup_restore_on_create": {"on"},
	}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	backup := cfg.Servers[0].Databases[0].Backup
	if backup == nil {
		t.Fatal("expected Backup to be non-nil after update")
	}
	if !backup.Enabled || backup.Schedule != "weekly" || backup.KeepCount != 5 || !backup.RestoreOnCreate {
		t.Errorf("unexpected backup config: %+v", backup)
	}
}

func TestUpdateBackup_DisableKeepsConfigNonNil(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigWithBackupJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":      {"0"},
		"db_index":          {"0"},
		"backup_schedule":   {"daily"},
		"backup_keep_count": {"7"},
	}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	backup := cfg.Servers[0].Databases[0].Backup
	if backup == nil {
		t.Fatal("expected Backup to stay non-nil (config block present, just disabled)")
	}
	if backup.Enabled {
		t.Error("expected Enabled to be false since backup_enabled was omitted from the form")
	}
	if backup.KeepCount != 7 {
		t.Errorf("expected KeepCount 7, got %d", backup.KeepCount)
	}
}

func TestUpdateBackup_ChangeSchedule(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigWithBackupJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":      {"0"},
		"db_index":          {"0"},
		"backup_enabled":    {"on"},
		"backup_schedule":   {"weekly"},
		"backup_keep_count": {"7"},
	}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Databases[0].Backup.Schedule != "weekly" {
		t.Errorf("expected schedule weekly, got %q", cfg.Servers[0].Databases[0].Backup.Schedule)
	}
}

func TestUpdateBackup_KeepCountDefaultsToZeroOnBadInput(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":      {"0"},
		"db_index":          {"0"},
		"backup_enabled":    {"on"},
		"backup_keep_count": {"not-a-number"},
	}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Databases[0].Backup.KeepCount != 0 {
		t.Errorf("expected KeepCount to default to 0, got %d", cfg.Servers[0].Databases[0].Backup.KeepCount)
	}
}

func TestUpdateBackup_InvalidServerIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{"server_index": {"99"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestUpdateBackup_InvalidDBIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	form := url.Values{"server_index": {"0"}, "db_index": {"99"}}
	req := httptest.NewRequest("POST", "/update-backup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddDatabase_WithBackupConfig(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index":             {"0"},
		"database":                 {"newdb"},
		"user":                     {"newuser"},
		"password":                 {"newpass"},
		"backup_enabled":           {"on"},
		"backup_schedule":          {"weekly"},
		"backup_keep_count":        {"3"},
		"backup_restore_on_create": {"on"},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	added := cfg.Servers[0].Databases[1]
	if added.Backup == nil {
		t.Fatal("expected Backup to be set")
	}
	if !added.Backup.Enabled || added.Backup.Schedule != "weekly" || added.Backup.KeepCount != 3 || !added.Backup.RestoreOnCreate {
		t.Errorf("unexpected backup config: %+v", added.Backup)
	}
}

func TestBackupOrDefault_NilReturnsDaily(t *testing.T) {
	got := backupOrDefault(nil)
	want := BackupConfig{Schedule: "daily"}
	if got != want {
		t.Errorf("backupOrDefault(nil) = %+v, want %+v", got, want)
	}
}

func TestBackupOrDefault_NonNilReturnsCopy(t *testing.T) {
	b := &BackupConfig{Enabled: true, Schedule: "weekly", KeepCount: 5, RestoreOnCreate: true}
	got := backupOrDefault(b)
	want := BackupConfig{Enabled: true, Schedule: "weekly", KeepCount: 5, RestoreOnCreate: true}
	if got != want {
		t.Errorf("backupOrDefault(non-nil) = %+v, want %+v", got, want)
	}
}

func TestIndex_ShowsBackupColumnDefaultsForNilBackup(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{`name="backup_enabled"`, `name="backup_schedule"`, `name="backup_keep_count"`, `name="backup_restore_on_create"`, `action="/update-backup"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
	if strings.Contains(body, `name="backup_enabled" checked`) {
		t.Error("did not expect backup_enabled to be checked for a nil Backup")
	}
}

func TestIndex_ShowsBackupColumnPopulatedForExistingBackup(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigWithBackupJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `name="backup_enabled" checked`) {
		t.Error("expected backup_enabled to be checked for an enabled Backup")
	}
	if !strings.Contains(body, `value="7"`) {
		t.Error("expected keep_count value 7 to be rendered")
	}
}

func TestIndex_AddDatabaseFormHasBackupFields(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	// The Add Database form and the per-row backup form share field names;
	// confirm both backup_schedule <select> blocks appear (one per database
	// row plus one in Add Database), i.e. at least 2 occurrences.
	if strings.Count(body, `name="backup_schedule"`) < 2 {
		t.Error("expected backup_schedule field in both the row form and the Add Database form")
	}
}

func TestAddDatabase_WithoutBackupConfigStaysNil(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"server_index": {"0"},
		"database":     {"newdb"},
		"user":         {"newuser"},
		"password":     {"newpass"},
	}
	req := httptest.NewRequest("POST", "/add-database", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	added := cfg.Servers[0].Databases[1]
	if added.Backup != nil {
		t.Errorf("expected Backup to stay nil when no backup fields are submitted, got %+v", added.Backup)
	}
}

func TestIndex_ShowsAddServerForm(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{
		`action="/add-server"`,
		`name="name"`,
		`name="root_connection_string"`,
		`name="dry_run"`,
		"Add Server",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
}

func postAdminForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestMigrateDatabase_DropRequiresTypedName(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	cfg := `{"servers":[
		{"name":"old-pg","root_connection_string":"postgres://r:p@old/postgres","databases":[{"database":"app","user":"app","password":"pw"}]},
		{"name":"new-pg","root_connection_string":"postgres://r:p@new/postgres","databases":[]}
	]}`
	for _, form := range []url.Values{
		{"confirm_drop": {"on"}},
		{"confirm_database": {"ap"}},
	} {
		path := makeTestConfig(t, cfg)
		form.Set("server_index", "0")
		form.Set("db_index", "0")
		form.Set("target_server", "new-pg")
		w := postAdminForm(t, newAdminHandler(path), "/migrate-database", form)
		if !strings.Contains(w.Header().Get("Location"), "Error") {
			t.Fatalf("form %v: expected error flash, got %q", form, w.Header().Get("Location"))
		}
		var out Config
		data, _ := os.ReadFile(path)
		json.Unmarshal(data, &out)
		if out.Servers[0].Databases[0].Migrate != nil {
			t.Fatalf("form %v: migrate block written despite bad confirmation", form)
		}
	}
}

func TestGeneratePassword_NeverEchoesPassword(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	w := postAdminForm(t, newAdminHandler(path), "/generate-password", url.Values{"server_index": {"0"}, "db_index": {"0"}})

	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	pw := out.Servers[0].Databases[0].Password
	if pw == "mypass" {
		t.Fatal("password was not changed")
	}
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	if strings.Contains(loc, pw) {
		t.Fatalf("redirect leaks the generated password: %q", loc)
	}
}

func TestRowActions_RejectStaleRow(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	// Page was rendered when row 0 was "otherdb"; the file has since changed.
	w := postAdminForm(t, newAdminHandler(path), "/update-password", url.Values{
		"server_index": {"0"}, "db_index": {"0"}, "database": {"otherdb"}, "new_password": {"x"},
	})
	if !strings.Contains(w.Header().Get("Location"), "Error") {
		t.Fatalf("expected stale-row error, got %q", w.Header().Get("Location"))
	}
	var out Config
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &out)
	if out.Servers[0].Databases[0].Password != "mypass" {
		t.Fatal("stale form changed the wrong database's password")
	}
}

func TestAddServer_RejectsDuplicateName(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	w := postAdminForm(t, newAdminHandler(path), "/add-server", url.Values{
		"name": {"Test Server"}, "root_connection_string": {"postgres://r:p@x/postgres"},
	})
	if !strings.Contains(w.Header().Get("Location"), "Error") {
		t.Fatalf("expected duplicate-name error, got %q", w.Header().Get("Location"))
	}
}

func TestRevealPassword_ShowsPasswordOnlyInResponse(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "mypass") {
		t.Fatal("index page renders the password without a reveal request")
	}
	if !strings.Contains(w.Body.String(), `action="/reveal-password"`) {
		t.Fatal("expected Show form when k8s secrets mode is off")
	}

	w = postAdminForm(t, h, "/reveal-password", url.Values{
		"server_index": {"0"}, "db_index": {"0"}, "database": {"mydb"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mypass") {
		t.Fatal("reveal response does not contain the password")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("expected Cache-Control: no-store, got %q", w.Header().Get("Cache-Control"))
	}
}

func TestRevealPassword_RejectedForSecretBackedDatabase(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	// A stray config password on a secret-backed row must still not be shown.
	h := newAdminHandler(makeTestConfig(t, strings.Replace(testConfigK8sJSON, `"k8s_secret": "mydb-credentials"`, `"k8s_secret": "mydb-credentials", "password": "mypass"`, 1)))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	w := postAdminForm(t, h, "/reveal-password", url.Values{"server_index": {"0"}, "db_index": {"0"}})
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "mypass") {
		t.Fatalf("expected 400 without password, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestRevealPassword_RejectsStaleRow(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	w := postAdminForm(t, newAdminHandler(makeTestConfig(t, testConfigJSON)), "/reveal-password", url.Values{
		"server_index": {"0"}, "db_index": {"0"}, "database": {"otherdb"},
	})
	if w.Code != http.StatusSeeOther || strings.Contains(w.Body.String(), "mypass") {
		t.Fatalf("expected stale-row redirect without password, got %d", w.Code)
	}
}

func TestBackupAge(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	today := time.Date(2026, 9, 27, 0, 0, 0, 0, time.Local)
	daily := DatabaseConfig{Database: "app", Backup: &BackupConfig{Enabled: true, Schedule: "daily"}}
	weekly := DatabaseConfig{Database: "app", Backup: &BackupConfig{Enabled: true, Schedule: "weekly"}}

	if state, _ := backupAge(cfgPath, "My Server", daily, today); state != "none" {
		t.Errorf("no files: got %q, want none", state)
	}

	dir := filepath.Join(filepath.Dir(cfgPath), "backups", slugify("My Server"), "app")
	os.MkdirAll(dir, 0o755)
	for _, name := range []string{"app_2026-09-20.sql.gz", "app_2026-09-24.sql.gz"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o600)
	}
	if state, age := backupAge(cfgPath, "My Server", daily, today); state != "overdue" || age != "3 days ago" {
		t.Errorf("daily, 3 days old: got %q %q, want overdue \"3 days ago\"", state, age)
	}
	if state, _ := backupAge(cfgPath, "My Server", weekly, today); state != "ok" {
		t.Errorf("weekly, 3 days old: got %q, want ok", state)
	}
	os.WriteFile(filepath.Join(dir, "app_2026-09-26.sql.gz"), nil, 0o600)
	if state, age := backupAge(cfgPath, "My Server", daily, today); state != "ok" || age != "yesterday" {
		t.Errorf("daily, yesterday: got %q %q, want ok yesterday", state, age)
	}
}

func TestAdmin_RejectsCrossSiteWrites(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))
	form := url.Values{"name": {"evil"}, "root_connection_string": {"postgres://u:p@evil:5432/postgres"}}

	for site, want := range map[string]int{"cross-site": http.StatusForbidden, "same-origin": http.StatusSeeOther} {
		req := httptest.NewRequest("POST", "/add-server", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", site)
		req.SetBasicAuth("admin", "secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("Sec-Fetch-Site %s: got %d, want %d", site, w.Code, want)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("Sec-Fetch-Site %s: missing X-Frame-Options", site)
		}
	}
}
