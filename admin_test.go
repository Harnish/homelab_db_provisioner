package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
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
