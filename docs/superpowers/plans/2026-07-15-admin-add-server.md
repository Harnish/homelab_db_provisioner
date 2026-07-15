# Admin UI: Add Database Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the admin UI add a brand-new `DatabaseServer` entry to the config, not just databases on existing servers.

**Architecture:** New `POST /add-server` handler in `admin.go` mirrors the existing `handleAddDatabase` handler: parse form, lock config, read+unmarshal file, append a new `DatabaseServer{Databases: []DatabaseConfig{}}`, marshal+write, redirect with flash message. A new "Add Server" fieldset is added to the inline admin template.

**Tech Stack:** Go stdlib `net/http`, `html/template`, existing `Config`/`DatabaseServer` types from `main.go`.

## Global Constraints

- Config writes always use `json.MarshalIndent(cfg, "", "  ")` + `os.WriteFile(path, out, 0600)` (spec, matches existing handlers).
- All admin routes stay behind `basicAuth` (already applied at the mux level in `newAdminHandler`).
- Handler must acquire `configMu.Lock()` (write lock) around read-modify-write, matching `handleAddDatabase`.
- Redirect pattern on completion: `http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)`; errors prefixed `"Error: "` so `FlashError` styling picks it up.
- New server is appended with `Databases: []DatabaseConfig{}` (empty) — per spec's accepted tradeoff, no extra validation added.

---

### Task 1: `handleAddServer` handler + route registration

**Files:**
- Modify: `admin.go` (add handler function near `handleAddDatabase` at admin.go:528-598; register route in `newAdminHandler` at admin.go:155-164)
- Test: `admin_test.go` (add tests near `TestAddDatabase_*` at admin_test.go:236-337)

**Interfaces:**
- Produces: `func handleAddServer(configPath string) http.HandlerFunc` — registered at `POST /add-server`.
- Consumes: existing `Config`, `DatabaseServer`, `DatabaseConfig` types from `main.go`; existing `configMu sync.RWMutex` from `main.go:20`.

- [ ] **Step 1: Write the failing tests**

Add to `admin_test.go` (place after `TestAddDatabase_InvalidServerIndex`, i.e. after line 337):

```go
func TestAddServer_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	form := url.Values{
		"name":                    {"New Server"},
		"root_connection_string":  {"postgres://root:pass@newhost/postgres"},
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
		"root_connection_string":  {"postgres://root:pass@newhost/postgres"},
		"dry_run":                 {"on"},
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
		"root_connection_string":  {"postgres://root:pass@newhost/postgres"},
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
		"root_connection_string":  {""},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestAddServer ./...`
Expected: FAIL — compile error, `undefined: handleAddServer` / route `/add-server` returns 404 (once handler exists but isn't registered) or build fails (handler doesn't exist yet).

- [ ] **Step 3: Implement `handleAddServer` and register the route**

In `admin.go`, add after `handleAddDatabase` (after the closing brace at what is currently line 598):

```go
func handleAddServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		name := r.FormValue("name")
		rootConnStr := r.FormValue("root_connection_string")
		if name == "" || rootConnStr == "" {
			http.Error(w, "name and root_connection_string are required", http.StatusBadRequest)
			return
		}

		newServer := DatabaseServer{
			Name:                 name,
			RootConnectionString: rootConnStr,
			DryRun:               r.FormValue("dry_run") == "on",
			Databases:            []DatabaseConfig{},
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
		cfg.Servers = append(cfg.Servers, newServer)

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Server added"), http.StatusSeeOther)
	}
}
```

In `admin.go`, in `newAdminHandler` (admin.go:155-164), add the route registration line after the `/add-database` line:

```go
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	mux.HandleFunc("/add-server", handleAddServer(configPath))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run TestAddServer ./...`
Expected: PASS (all 5 new tests)

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS (no regressions)

- [ ] **Step 6: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add handler for creating new database servers via admin UI"
```

---

### Task 2: "Add Server" form in admin template

**Files:**
- Modify: `admin.go` (template string at admin.go:31-145)
- Test: `admin_test.go` (add test near `TestIndex_AddDatabaseFormHasBackupFields`)

**Interfaces:**
- Consumes: `adminTemplateData.Servers` (already populated by `handleIndex`, admin.go:212).
- Produces: nothing consumed by later tasks — this is the last task in the plan.

- [ ] **Step 1: Write the failing test**

Add to `admin_test.go` (place after the last existing test in the file):

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestIndex_ShowsAddServerForm ./...`
Expected: FAIL — body does not contain `action="/add-server"` etc.

- [ ] **Step 3: Add the "Add Server" fieldset to the template**

In `admin.go`, in the template string (admin.go:31-145), insert a new section right before the `<h2>Add Database</h2>` block (before line 117):

```html
  <h2>Add Server</h2>
  <form method="POST" action="/add-server">
    <fieldset>
      <legend>New Server Entry</legend>
      <label>Name: <input type="text" name="name" required></label>
      <label>Root connection string: <input type="text" name="root_connection_string" placeholder="postgres://user:pass@host:5432/postgres" required></label>
      <label><input type="checkbox" name="dry_run"> Dry run</label>
      <button type="submit">Add Server</button>
    </fieldset>
  </form>

```

(This goes directly above the existing `<h2>Add Database</h2>` line.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestIndex_ShowsAddServerForm ./...`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS (no regressions)

- [ ] **Step 6: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add Add Server form to admin UI template"
```

---

## Manual verification (post-implementation)

1. `ADMIN_SITE=true ADMIN_USER=admin ADMIN_PASSWORD=secret WATCH_MODE=true CONFIG_PATH=./config.json go run .`
2. Open `http://localhost:8080`, log in.
3. Submit "Add Server" with a name and connection string.
4. Confirm redirect shows "Server added" flash, new server appears in "Current Databases" section (with no databases), and appears in the "Add Database" server dropdown.
5. Check `config.json` on disk has the new server entry with `"databases": []`.
