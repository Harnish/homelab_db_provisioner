# Admin UI Backup Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the admin web UI view and edit each database's backup settings (`enabled`, `schedule`, `keep_count`, `restore_on_create`) instead of requiring a hand-edit of `config.json`.

**Architecture:** A single shared `parseBackupFields(r *http.Request) BackupConfig` helper parses the four backup form fields from any POST request. A new `POST /update-backup` handler always writes a non-nil `*BackupConfig` for the targeted database (the row's Save button always reflects current form state). `handleAddDatabase` reuses the same helper but only attaches a `*BackupConfig` if the parsed fields are non-default (nil-if-untouched, matching today's behavior for databases added without a `backup` block). The template gets a `backupOrDefault(*BackupConfig) BackupConfig` helper function so it never has to branch on a nil pointer — it always renders from a concrete `BackupConfig` value (defaults: `Schedule: "daily"`, everything else zero).

**Tech Stack:** Go 1.26 (existing), `html/template`, `net/http` — no new dependencies.

## Global Constraints

- `BackupConfig` struct is unchanged (`main.go`): `Enabled bool`, `Schedule string`, `KeepCount int`, `RestoreOnCreate bool`. `DatabaseConfig.Backup *BackupConfig` is unchanged.
- No changes to `backup.go` — `runBackups`/`findNewestBackup`/`pruneBackups` already consume `BackupConfig` as-is.
- No JavaScript. The admin UI stays plain server-rendered HTML.
- `Keep Count` parse failure defaults to `0` (keep all) — never a 400, matching the existing "0 = keep all" semantics documented for `keep_count`.
- Editing an existing database's backup settings via `/update-backup` always results in a non-nil `Backup` (the row's form always submits current state, even all-disabled/all-zero).
- Adding a new database via `/add-database` with all backup fields left at their default/unchecked state results in a nil `Backup` (unchanged behavior from before this feature).
- `go test ./...` must pass at every commit in this plan; existing tests (`TestAddDatabase_Success`, `TestAddDatabase_EmptyPermissions`, etc.) must keep passing unmodified.

---

### Task 1: `parseBackupFields` helper + `handleUpdateBackup` handler

**Files:**
- Modify: `admin.go` (add helper + handler + route registration)
- Test: `admin_test.go`

**Interfaces:**
- Produces: `func parseBackupFields(r *http.Request) BackupConfig` — used by Task 2 (`handleAddDatabase`) and Task 3 (indirectly, via the form fields it reads matching the template's field names).
- Produces: `POST /update-backup` route, handled by `handleUpdateBackup(configPath string) http.HandlerFunc`.

- [ ] **Step 1: Write the failing tests**

Add to `admin_test.go` (this file already has `testConfigJSON` with one server/one database with `Backup: nil`; add a second fixture with an existing backup config for the disable/change tests):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestUpdateBackup -v`
Expected: FAIL — `404 page not found` (route doesn't exist yet) or compile error if `parseBackupFields`/`handleUpdateBackup` are referenced anywhere yet (they aren't, so this should just 404 at the HTTP layer since `newAdminHandler`'s mux has no `/update-backup` route).

- [ ] **Step 3: Implement `parseBackupFields` and `handleUpdateBackup`**

Add to `admin.go`, after `handleGeneratePassword` and before `handleRotateSecret`:

```go
func parseBackupFields(r *http.Request) BackupConfig {
	schedule := r.FormValue("backup_schedule")
	if schedule == "" {
		schedule = "daily"
	}
	keepCount := 0
	if s := strings.TrimSpace(r.FormValue("backup_keep_count")); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			keepCount = n
		}
	}
	return BackupConfig{
		Enabled:         r.FormValue("backup_enabled") == "on",
		Schedule:        schedule,
		KeepCount:       keepCount,
		RestoreOnCreate: r.FormValue("backup_restore_on_create") == "on",
	}
}

func handleUpdateBackup(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
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

		backup := parseBackupFields(r)

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
		cfg.Servers[si].Databases[di].Backup = &backup

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Backup settings updated"), http.StatusSeeOther)
	}
}
```

- [ ] **Step 4: Register the route**

In `admin.go`, change:

```go
func newAdminHandler(configPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex(configPath))
	mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
	mux.HandleFunc("/generate-password", handleGeneratePassword(configPath))
	mux.HandleFunc("/rotate-secret", handleRotateSecret(configPath))
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	return basicAuth(mux)
}
```

to:

```go
func newAdminHandler(configPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex(configPath))
	mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
	mux.HandleFunc("/generate-password", handleGeneratePassword(configPath))
	mux.HandleFunc("/rotate-secret", handleRotateSecret(configPath))
	mux.HandleFunc("/update-backup", handleUpdateBackup(configPath))
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	return basicAuth(mux)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run TestUpdateBackup -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 6: Run the full suite to confirm no regressions**

Run: `go test ./... -v`
Expected: all tests pass, including every pre-existing test.

- [ ] **Step 7: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add admin UI handler for editing per-database backup settings"
```

---

### Task 2: Backup fields on Add Database

**Files:**
- Modify: `admin.go` (`handleAddDatabase`)
- Test: `admin_test.go`

**Interfaces:**
- Consumes: `parseBackupFields(r *http.Request) BackupConfig` (Task 1).

- [ ] **Step 1: Write the failing tests**

Add to `admin_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestAddDatabase_WithBackupConfig -v`
Expected: FAIL — `TestAddDatabase_WithBackupConfig` fails because `added.Backup` is nil (handler doesn't parse backup fields yet). `TestAddDatabase_WithoutBackupConfigStaysNil` passes trivially already (no code changed yet), which is fine — it documents the pre-existing behavior you must not break.

- [ ] **Step 3: Update `handleAddDatabase`**

In `admin.go`, change:

```go
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, DatabaseConfig{
			Database:    database,
			User:        user,
			Password:    r.FormValue("password"),
			Permissions: permissions,
		})
```

to:

```go
		newDB := DatabaseConfig{
			Database:    database,
			User:        user,
			Password:    r.FormValue("password"),
			Permissions: permissions,
		}
		if backup := parseBackupFields(r); backup.Enabled || backup.RestoreOnCreate || backup.KeepCount != 0 {
			newDB.Backup = &backup
		}
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, newDB)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestAddDatabase -v`
Expected: PASS (all `TestAddDatabase_*` tests, including the two new ones and the three pre-existing ones: `TestAddDatabase_Success`, `TestAddDatabase_EmptyPermissions`, `TestAddDatabase_InvalidServerIndex`).

- [ ] **Step 5: Run the full suite**

Run: `go test ./... -v`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: parse backup settings on Add Database form"
```

---

### Task 3: Template — Backup column + Add Database fields

**Files:**
- Modify: `admin.go` (template string, `FuncMap`)
- Test: `admin_test.go`

**Interfaces:**
- Produces: `func backupOrDefault(b *BackupConfig) BackupConfig` — template-only helper, no other task depends on it.

- [ ] **Step 1: Write the failing tests**

Add to `admin_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestBackupOrDefault|TestIndex_ShowsBackupColumn|TestIndex_AddDatabaseFormHasBackupFields' -v`
Expected: FAIL — `undefined: backupOrDefault` (compile error) and/or missing strings in rendered body once it compiles with a stub.

- [ ] **Step 3: Add `backupOrDefault` and register it in the `FuncMap`**

In `admin.go`, change:

```go
var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":       strings.Join,
	"secretName": secretNameFor,
}).Parse(`<!DOCTYPE html>
```

to:

```go
var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":            strings.Join,
	"secretName":      secretNameFor,
	"backupOrDefault": backupOrDefault,
}).Parse(`<!DOCTYPE html>
```

Add the function anywhere in `admin.go` above `var adminTemplate` (Go doesn't require a specific order, but keep it readable — e.g. just before `var adminTemplate`):

```go
func backupOrDefault(b *BackupConfig) BackupConfig {
	if b == nil {
		return BackupConfig{Schedule: "daily"}
	}
	return *b
}
```

- [ ] **Step 4: Add the Backup column to the table**

In `admin.go`'s template string, change the table header row:

```html
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>{{if $.K8sEnabled}}Kubernetes Secret{{else}}Change Password{{end}}</th></tr>
```

to:

```html
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>{{if $.K8sEnabled}}Kubernetes Secret{{else}}Change Password{{end}}</th><th>Backup</th></tr>
```

Then, right after the closing `</td>` of the password/secret column's `<td>...</td>` block (i.e. right before the row's closing `</tr>`), add a new `<td>`:

```html
      {{range $di, $db := $server.Databases}}
      <tr>
        <td>{{$db.Database}}</td>
        <td>{{$db.User}}</td>
        <td>{{if $db.Permissions}}{{join $db.Permissions ", "}}{{else}}ALL{{end}}</td>
        <td>
          {{if $.K8sEnabled}}
            <code>{{secretName $server.Name $db.Database}}</code>
            <form method="POST" action="/rotate-secret" style="display:inline;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <button type="submit">Rotate</button>
            </form>
          {{else}}
            <form method="POST" action="/update-password" style="display:inline-flex;gap:0.25rem;align-items:center;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="password" name="new_password" placeholder="New password" required>
              <button type="submit">Update</button>
            </form>
            <form method="POST" action="/generate-password" style="display:inline;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <button type="submit">Generate</button>
            </form>
          {{end}}
        </td>
        <td>
          {{$backup := backupOrDefault $db.Backup}}
          <form method="POST" action="/update-backup" style="display:inline-flex;gap:0.4rem;align-items:center;flex-wrap:wrap;">
            <input type="hidden" name="server_index" value="{{$si}}">
            <input type="hidden" name="db_index" value="{{$di}}">
            <label style="display:inline;margin:0;"><input type="checkbox" name="backup_enabled" {{if $backup.Enabled}}checked{{end}}> Enabled</label>
            <select name="backup_schedule">
              <option value="daily" {{if eq $backup.Schedule "daily"}}selected{{end}}>daily</option>
              <option value="weekly" {{if eq $backup.Schedule "weekly"}}selected{{end}}>weekly</option>
            </select>
            <input type="number" name="backup_keep_count" value="{{$backup.KeepCount}}" min="0" style="width:60px;">
            <label style="display:inline;margin:0;"><input type="checkbox" name="backup_restore_on_create" {{if $backup.RestoreOnCreate}}checked{{end}}> Restore on Create</label>
            <button type="submit">Save</button>
          </form>
        </td>
      </tr>
      {{end}}
```

- [ ] **Step 5: Add backup fields to the Add Database form**

In `admin.go`'s template string, change:

```html
      <label>Permissions (comma-separated, blank for ALL): <input type="text" name="permissions"></label>
      <button type="submit">Add Database</button>
```

to:

```html
      <label>Permissions (comma-separated, blank for ALL): <input type="text" name="permissions"></label>
      <label><input type="checkbox" name="backup_enabled"> Enable backups</label>
      <label>Backup schedule:
        <select name="backup_schedule">
          <option value="daily">daily</option>
          <option value="weekly">weekly</option>
        </select>
      </label>
      <label>Backup keep count (0 = keep all): <input type="number" name="backup_keep_count" value="0" min="0"></label>
      <label><input type="checkbox" name="backup_restore_on_create"> Restore newest backup on create</label>
      <button type="submit">Add Database</button>
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: all tests pass, including every test from Tasks 1-2 and the new ones from this task. Pay particular attention to `TestIndex_ShowsDatabasesAndForms` (pre-existing) and `TestIndex_ShowsK8sSecretColumnWhenEnabled`/`TestIndex_HidesK8sSecretColumnWhenDisabled` (pre-existing, from the Kubernetes Secrets feature) — these must still pass since the Backup column is additive, not a replacement of the password/secret column.

- [ ] **Step 7: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: add backup settings column and Add Database fields to admin UI"
```

---

## Self-Review Notes

- **Spec coverage:** Backup column with inline edit (Task 3) ✅, Add Database backup fields (Task 2 + Task 3 Step 5) ✅, backend handler + nil-vs-non-nil rules (Task 1 + Task 2) ✅, no `backup.go` changes ✅, no JS ✅.
- **Type consistency:** `parseBackupFields` returns `BackupConfig` (value, not pointer) consistently in Tasks 1-2; `backupOrDefault` also returns `BackupConfig` (value) consistently in Task 3 — no signature drift between tasks.
- **Test coverage:** every new code path (enable-from-nil, disable-keeps-non-nil, schedule change, bad keep_count input, invalid indices, add-with-backup, add-without-backup, template defaults, template populated, add-form fields present) has a corresponding test.
