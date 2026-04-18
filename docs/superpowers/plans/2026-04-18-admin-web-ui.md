# Admin Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Basic-Auth-protected HTTP admin UI that lets users add new database entries and change passwords in the config file, with watch mode applying the changes automatically.

**Architecture:** A new `admin.go` file exposes `newAdminHandler(configPath string) http.Handler` (for testability) and `startAdminServer(configPath string)` (called from `main()`). A package-level `sync.RWMutex` guards all config file access — watch mode and `runOnce` use `RLock`, admin POST handlers use `Lock`. The server only starts when `ADMIN_SITE=true`, `ADMIN_USER`, and `ADMIN_PASSWORD` are all set.

**Tech Stack:** Go 1.21 stdlib — `net/http`, `html/template`, `sync`, `encoding/json`, `net/http/httptest` (tests)

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `main.go` | Modify | Add `var configMu sync.RWMutex`, wrap `loadConfig()` calls with `RLock`, start admin goroutine |
| `admin.go` | Create | HTTP server, Basic Auth middleware, all route handlers, embedded HTML template |
| `admin_test.go` | Create | httptest-based tests for auth, GET /, POST /update-password, POST /add-database |

---

## Task 1: Add configMu to main.go and protect config reads

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Add the mutex and update imports**

In `main.go`, add `"sync"` to the import block and declare the mutex at package level (above `func main`):

```go
import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "strings"
    "sync"
    "time"

    _ "github.com/go-sql-driver/mysql"
    _ "github.com/lib/pq"
)

var configMu sync.RWMutex
```

- [ ] **Step 2: Wrap loadConfig calls in runOnce and runWatchMode**

Replace the `loadConfig()` call in `runOnce`:

```go
func runOnce() {
    configMu.RLock()
    config, err := loadConfig()
    configMu.RUnlock()
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    if err := processConfig(config); err != nil {
        log.Fatalf("Failed to process config: %v", err)
    }

    log.Println("Database provisioning completed")
}
```

Replace the `loadConfig()` call inside the `if currentModTime.After(lastModTime)` block in `runWatchMode`:

```go
configMu.RLock()
config, err := loadConfig()
configMu.RUnlock()
```

- [ ] **Step 3: Build to verify no compile errors**

```bash
go build ./...
```

Expected: no output (clean build).

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: add configMu mutex to guard config file access"
```

---

## Task 2: Create admin.go skeleton with Basic Auth and routing

**Files:**
- Create: `admin_test.go`
- Create: `admin.go`

- [ ] **Step 1: Write failing Basic Auth tests**

Create `admin_test.go`:

```go
package main

import (
    "net/http"
    "net/http/httptest"
    "os"
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./... -run "TestBasicAuth" -v
```

Expected: compile error — `newAdminHandler` undefined.

- [ ] **Step 3: Create admin.go with skeleton**

Create `admin.go`:

```go
package main

import (
    "encoding/json"
    "html/template"
    "log"
    "net/http"
    "net/url"
    "os"
    "strconv"
    "strings"
)

var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
    "join": strings.Join,
}).Parse(`<!DOCTYPE html>
<html>
<head>
  <title>DB Provisioner Admin</title>
  <style>
    body { font-family: sans-serif; max-width: 960px; margin: 2rem auto; padding: 0 1rem; }
    table { border-collapse: collapse; width: 100%; margin-bottom: 1rem; }
    th, td { border: 1px solid #ccc; padding: 0.5rem; text-align: left; }
    .flash { padding: 0.5rem 1rem; border-radius: 4px; margin-bottom: 1rem; }
    .flash-ok { background: #d4edda; color: #155724; }
    .flash-err { background: #f8d7da; color: #721c24; }
    fieldset { margin-bottom: 1rem; }
    label { display: block; margin-bottom: 0.4rem; }
    input[type=text], input[type=password], select { width: 300px; padding: 0.2rem; }
  </style>
</head>
<body>
  <h1>DB Provisioner Admin</h1>
  {{if .Flash}}
    <div class="flash {{if .FlashError}}flash-err{{else}}flash-ok{{end}}">{{.Flash}}</div>
  {{end}}

  <h2>Current Databases</h2>
  {{range $si, $server := .Servers}}
    <h3>{{$server.Name}}</h3>
    <table>
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>Change Password</th></tr>
      {{range $di, $db := $server.Databases}}
      <tr>
        <td>{{$db.Database}}</td>
        <td>{{$db.User}}</td>
        <td>{{if $db.Permissions}}{{join $db.Permissions ", "}}{{else}}ALL{{end}}</td>
        <td>
          <form method="POST" action="/update-password">
            <input type="hidden" name="server_index" value="{{$si}}">
            <input type="hidden" name="db_index" value="{{$di}}">
            <input type="password" name="new_password" placeholder="New password" required>
            <button type="submit">Update</button>
          </form>
        </td>
      </tr>
      {{end}}
    </table>
  {{end}}

  <h2>Add Database</h2>
  <form method="POST" action="/add-database">
    <fieldset>
      <legend>New Database Entry</legend>
      <label>Server:
        <select name="server_index">
          {{range $si, $server := .Servers}}
          <option value="{{$si}}">{{$server.Name}}</option>
          {{end}}
        </select>
      </label>
      <label>Database name: <input type="text" name="database" required></label>
      <label>Username: <input type="text" name="user" required></label>
      <label>Password: <input type="password" name="password" required></label>
      <label>Permissions (comma-separated, blank for ALL): <input type="text" name="permissions"></label>
      <button type="submit">Add Database</button>
    </fieldset>
  </form>
</body>
</html>`))

type adminTemplateData struct {
    Servers    []DatabaseServer
    Flash      string
    FlashError bool
}

func newAdminHandler(configPath string) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("/", handleIndex(configPath))
    mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
    mux.HandleFunc("/add-database", handleAddDatabase(configPath))
    return basicAuth(mux)
}

func startAdminServer(configPath string) {
    port := os.Getenv("ADMIN_PORT")
    if port == "" {
        port = "8080"
    }
    log.Printf("Admin server listening on :%s", port)
    if err := http.ListenAndServe(":"+port, newAdminHandler(configPath)); err != nil {
        log.Fatalf("Admin server error: %v", err)
    }
}

func basicAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user, pass, ok := r.BasicAuth()
        if !ok || user != os.Getenv("ADMIN_USER") || pass != os.Getenv("ADMIN_PASSWORD") {
            w.Header().Set("WWW-Authenticate", `Basic realm="Admin"`)
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}

func handleIndex(configPath string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
            http.NotFound(w, r)
            return
        }
        configMu.RLock()
        data, err := os.ReadFile(configPath)
        configMu.RUnlock()
        if err != nil {
            http.Error(w, "Failed to read config: "+err.Error(), http.StatusInternalServerError)
            return
        }
        var cfg Config
        if err := json.Unmarshal(data, &cfg); err != nil {
            http.Error(w, "Failed to parse config: "+err.Error(), http.StatusInternalServerError)
            return
        }
        msg := r.URL.Query().Get("msg")
        adminTemplate.Execute(w, adminTemplateData{
            Servers:    cfg.Servers,
            Flash:      msg,
            FlashError: strings.HasPrefix(msg, "Error:"),
        })
    }
}

func handleUpdatePassword(configPath string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        r.ParseForm()
        si, err := strconv.Atoi(r.FormValue("server_index"))
        if err != nil {
            http.Error(w, "Invalid server_index", http.StatusBadRequest)
            return
        }
        di, err := strconv.Atoi(r.FormValue("db_index"))
        if err != nil {
            http.Error(w, "Invalid db_index", http.StatusBadRequest)
            return
        }
        newPassword := r.FormValue("new_password")

        configMu.Lock()
        defer configMu.Unlock()

        fileData, err := os.ReadFile(configPath)
        if err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
            return
        }
        var cfg Config
        if err := json.Unmarshal(fileData, &cfg); err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
            return
        }
        if si < 0 || si >= len(cfg.Servers) {
            http.Error(w, "server_index out of range", http.StatusBadRequest)
            return
        }
        if di < 0 || di >= len(cfg.Servers[si].Databases) {
            http.Error(w, "db_index out of range", http.StatusBadRequest)
            return
        }
        cfg.Servers[si].Databases[di].Password = newPassword

        out, err := json.MarshalIndent(cfg, "", "  ")
        if err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
            return
        }
        if err := os.WriteFile(configPath, out, 0644); err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
            return
        }
        http.Redirect(w, r, "/?msg="+url.QueryEscape("Password updated"), http.StatusSeeOther)
    }
}

func handleAddDatabase(configPath string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        r.ParseForm()
        si, err := strconv.Atoi(r.FormValue("server_index"))
        if err != nil {
            http.Error(w, "Invalid server_index", http.StatusBadRequest)
            return
        }

        var permissions []string
        if p := strings.TrimSpace(r.FormValue("permissions")); p != "" {
            for _, perm := range strings.Split(p, ",") {
                permissions = append(permissions, strings.TrimSpace(perm))
            }
        }

        configMu.Lock()
        defer configMu.Unlock()

        fileData, err := os.ReadFile(configPath)
        if err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
            return
        }
        var cfg Config
        if err := json.Unmarshal(fileData, &cfg); err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
            return
        }
        if si < 0 || si >= len(cfg.Servers) {
            http.Error(w, "server_index out of range", http.StatusBadRequest)
            return
        }

        cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, DatabaseConfig{
            Database:    r.FormValue("database"),
            User:        r.FormValue("user"),
            Password:    r.FormValue("password"),
            Permissions: permissions,
        })

        out, err := json.MarshalIndent(cfg, "", "  ")
        if err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
            return
        }
        if err := os.WriteFile(configPath, out, 0644); err != nil {
            http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
            return
        }
        http.Redirect(w, r, "/?msg="+url.QueryEscape("Database added"), http.StatusSeeOther)
    }
}
```

- [ ] **Step 4: Run Basic Auth tests to verify they pass**

```bash
go test ./... -run "TestBasicAuth" -v
```

Expected:
```
--- PASS: TestBasicAuth_Unauthorized (0.00s)
--- PASS: TestBasicAuth_WrongPassword (0.00s)
--- PASS: TestBasicAuth_Authorized (0.00s)
```

- [ ] **Step 5: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add admin HTTP server with Basic Auth middleware"
```

---

## Task 3: Test GET / — page content and flash messages

**Files:**
- Modify: `admin_test.go`

- [ ] **Step 1: Add GET / tests to admin_test.go**

Append to `admin_test.go`:

```go
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
```

Add `"strings"` to the import block in `admin_test.go` if not already present.

- [ ] **Step 2: Run the new tests**

```bash
go test ./... -run "TestIndex" -v
```

Expected:
```
--- PASS: TestIndex_ShowsDatabasesAndForms (0.00s)
--- PASS: TestIndex_ShowsFlashMessage (0.00s)
--- PASS: TestIndex_UnknownPathReturns404 (0.00s)
```

- [ ] **Step 3: Commit**

```bash
git add admin_test.go
git commit -m "test: add GET / handler tests for admin UI"
```

---

## Task 4: Test POST /update-password

**Files:**
- Modify: `admin_test.go`

- [ ] **Step 1: Add update-password tests to admin_test.go**

Add `"encoding/json"`, `"net/url"` to the import block in `admin_test.go`, then append:

```go
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
```

- [ ] **Step 2: Run the new tests**

```bash
go test ./... -run "TestUpdatePassword" -v
```

Expected:
```
--- PASS: TestUpdatePassword_Success (0.00s)
--- PASS: TestUpdatePassword_InvalidServerIndex (0.00s)
--- PASS: TestUpdatePassword_InvalidDBIndex (0.00s)
```

- [ ] **Step 3: Commit**

```bash
git add admin_test.go
git commit -m "test: add POST /update-password handler tests"
```

---

## Task 5: Test POST /add-database

**Files:**
- Modify: `admin_test.go`

- [ ] **Step 1: Add add-database tests to admin_test.go**

Append to `admin_test.go`:

```go
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
    json.Unmarshal(data, &cfg)
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
```

- [ ] **Step 2: Run the new tests**

```bash
go test ./... -run "TestAddDatabase" -v
```

Expected:
```
--- PASS: TestAddDatabase_Success (0.00s)
--- PASS: TestAddDatabase_EmptyPermissions (0.00s)
--- PASS: TestAddDatabase_InvalidServerIndex (0.00s)
```

- [ ] **Step 3: Run the full test suite**

```bash
go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add admin_test.go
git commit -m "test: add POST /add-database handler tests"
```

---

## Task 6: Wire admin server into main()

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Update main() to start the admin server goroutine**

Replace the `func main()` body in `main.go` with:

```go
func main() {
    log.Println("PostgreSQL Database Provisioner starting...")

    if os.Getenv("ADMIN_SITE") == "true" {
        adminUser := os.Getenv("ADMIN_USER")
        adminPass := os.Getenv("ADMIN_PASSWORD")
        if adminUser == "" || adminPass == "" {
            log.Fatal("ADMIN_SITE=true requires ADMIN_USER and ADMIN_PASSWORD to be set")
        }
        go startAdminServer(getConfigPath())
    }

    watchMode := os.Getenv("WATCH_MODE")
    if watchMode == "true" {
        runWatchMode()
    } else {
        runOnce()
    }
}
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 3: Run the full test suite**

```bash
go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 4: Smoke test — start the server locally**

In one terminal, create a test config and start the server:

```bash
cp config.json /tmp/test-admin-config.json
ADMIN_SITE=true ADMIN_USER=admin ADMIN_PASSWORD=secret WATCH_MODE=true CONFIG_PATH=/tmp/test-admin-config.json go run .
```

In another terminal, verify auth works:

```bash
# Should return 401
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/

# Should return 200
curl -s -o /dev/null -w "%{http_code}" -u admin:secret http://localhost:8080/
```

Expected: `401` then `200`.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: wire admin server into main with env var gating"
```
