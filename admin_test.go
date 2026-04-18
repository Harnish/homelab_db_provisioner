package main

import (
	"net/http"
	"net/http/httptest"
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
