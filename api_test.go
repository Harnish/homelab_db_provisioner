package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAPIListServers_RequiresAuth(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/api/servers", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAPIListServers_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/api/servers", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Servers []serverResponse `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 1 || body.Servers[0].Name != "Test Server" {
		t.Errorf("unexpected servers: %+v", body.Servers)
	}
	if len(body.Servers[0].Databases) != 1 || body.Servers[0].Databases[0].Database != "mydb" {
		t.Errorf("unexpected nested databases: %+v", body.Servers[0].Databases)
	}
}

func TestAPIGetServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/api/servers/0", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got serverResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Test Server" {
		t.Errorf("unexpected server: %+v", got)
	}
}

func TestAPIGetServer_NotFound(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("GET", "/api/servers/99", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestAPICreateServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	body := `{"name":"New Server","root_connection_string":"postgres://root:pass@newhost/postgres","dry_run":true}`
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
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
	if added.Name != "New Server" || !added.DryRun || len(added.Databases) != 0 {
		t.Errorf("unexpected server entry: %+v", added)
	}
}

func TestAPICreateServer_MissingFields(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	body := `{"name":""}`
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAPIUpdateServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	body := `{"dry_run":true}`
	req := httptest.NewRequest("PATCH", "/api/servers/0", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Servers[0].DryRun {
		t.Error("expected dry_run true")
	}
	if cfg.Servers[0].Name != "Test Server" {
		t.Errorf("expected name unchanged, got %q", cfg.Servers[0].Name)
	}
}

func TestAPIUpdateServer_NotFound(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	body := `{"dry_run":true}`
	req := httptest.NewRequest("PATCH", "/api/servers/99", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestAPIDeleteServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	req := httptest.NewRequest("DELETE", "/api/servers/0", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(cfg.Servers))
	}
}

func TestAPIDeleteServer_NotFound(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	req := httptest.NewRequest("DELETE", "/api/servers/99", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
