package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	for _, want := range []string{"Test Server", "mydb", "myuser", "Add Database", "Change Password"} {
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
		"name":                    {"New Server"},
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
		"name":                    {"New Server"},
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
		"name":                    {""},
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
		"name":                    {"New Server"},
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

func TestIndex_ShowsK8sSecretColumnWhenEnabled(t *testing.T) {
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
	wantName := secretNameFor("Test Server", "mydb")
	for _, want := range []string{wantName, "Rotate", "Kubernetes Secret"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
	if strings.Contains(body, `action="/update-password"`) {
		t.Error("did not expect manual password form when k8s secrets enabled")
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
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), secretNameFor("Test Server", "mydb"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if len(secret.Data["password"]) == 0 {
		t.Error("expected password to be set on the secret")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Databases[0].Password != "mypass" {
		t.Errorf("expected config.json password untouched, got %q", cfg.Servers[0].Databases[0].Password)
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
	        {"database": "mydb", "user": "myuser", "password": "mypass"}
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
