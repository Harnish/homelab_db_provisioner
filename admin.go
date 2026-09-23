package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func backupOrDefault(b *BackupConfig) BackupConfig {
	if b == nil {
		return BackupConfig{Schedule: "daily"}
	}
	return *b
}

var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":            strings.Join,
	"secretName":      secretNameFor,
	"backupOrDefault": backupOrDefault,
	"dbType": func(connStr string) string {
		switch detectDBType(connStr) {
		case MariaDB:
			return "mariadb"
		case MongoDB:
			return "mongodb"
		default:
			return "postgres"
		}
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>DB Provisioner Admin</title>
  <style>
    * { box-sizing: border-box; }
    body { font-family: sans-serif; max-width: 1280px; margin: 2rem auto; padding: 0 1rem; }
    table { border-collapse: collapse; width: 100%; margin-bottom: 1rem; }
    th, td { border: 1px solid #ccc; padding: 0.5rem; text-align: left; vertical-align: top; overflow-wrap: break-word; }
    .table-scroll { overflow-x: auto; margin-bottom: 1rem; }
    .table-scroll table { margin-bottom: 0; }
    td input[type=password], td select { width: auto; }
    td label { white-space: nowrap; }
    .empty { color: #555; font-style: italic; }
    .notice { padding: 0.5rem 1rem; border-radius: 4px; margin-bottom: 1rem; background: #fff3cd; color: #664d03; }
    .flash { padding: 0.5rem 1rem; border-radius: 4px; margin-bottom: 1rem; }
    .flash-ok { background: #d4edda; color: #155724; }
    .flash-err { background: #f8d7da; color: #721c24; }
    fieldset { margin-bottom: 1rem; }
    label { display: block; margin-bottom: 0.4rem; }
    input[type=text], input[type=password], select { width: 100%; max-width: 300px; padding: 0.2rem; }
    .layout { display: flex; flex-wrap: wrap; gap: 2rem; align-items: flex-start; }
    .col-left { flex: 2 1 36rem; min-width: 0; }
    .col-right { flex: 1 1 280px; }
  </style>
</head>
<body>
  <h1>DB Provisioner Admin</h1>
  {{if .Flash}}
    <div class="flash {{if .FlashError}}flash-err{{else}}flash-ok{{end}}" role="{{if .FlashError}}alert{{else}}status{{end}}">{{.Flash}}</div>
  {{end}}
  {{if not .WatchMode}}
    <div class="notice" role="note">Watch mode is off. Changes made here are saved to the config file but won't be applied to your databases until the provisioner runs again.</div>
  {{end}}

  <div class="layout">
  <div class="col-left">
  <h2>Current Databases</h2>
  {{range $si, $server := .Servers}}
    <h3>{{$server.Name}}</h3>
    {{if not $server.Databases}}
    <p class="empty">No databases on this server yet.</p>
    {{else}}
    <div class="table-scroll">
    <table>
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>Extensions</th><th>{{if $.K8sEnabled}}Kubernetes Secret{{else}}Change Password{{end}}</th><th>Backup</th><th>Migrate</th></tr>
      {{range $di, $db := $server.Databases}}
      <tr>
        <td>{{$db.Database}}</td>
        <td>{{$db.User}}</td>
        <td>{{if $db.Permissions}}{{join $db.Permissions ", "}}{{else}}ALL{{end}}</td>
        <td>{{if $db.Extensions}}{{join $db.Extensions ", "}}{{else}}—{{end}}</td>
        <td>
          {{if $.K8sEnabled}}
            <code>{{secretName $server.Name $db.Database}}</code>
            {{if $db.RequiresConnectString}}<br><small>+ connection_string</small>{{end}}
            <form method="POST" action="/rotate-secret" style="display:inline;" onsubmit="return confirm('Rotate the secret for {{$db.Database}}? Apps using the old password will fail until they reload the secret.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <button type="submit">Rotate</button>
            </form>
          {{else}}
            <form method="POST" action="/update-password" style="display:inline-flex;gap:0.25rem;align-items:center;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <input type="password" name="new_password" placeholder="New password" aria-label="New password for {{$db.Database}}" autocomplete="new-password" required>
              <button type="submit">Update</button>
            </form>
            <form method="POST" action="/generate-password" style="display:inline;" onsubmit="return confirm('Replace the password for {{$db.Database}} with a generated one? Apps using the old password will fail until they are updated.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <button type="submit">Generate</button>
            </form>
          {{end}}
        </td>
        <td>
          {{$backup := backupOrDefault $db.Backup}}
          <form method="POST" action="/update-backup" style="display:inline-flex;gap:0.4rem;align-items:center;flex-wrap:wrap;">
            <input type="hidden" name="server_index" value="{{$si}}">
            <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
            <label style="display:inline;margin:0;"><input type="checkbox" name="backup_enabled" {{if $backup.Enabled}}checked{{end}}> Enabled</label>
            <select name="backup_schedule" aria-label="Backup schedule for {{$db.Database}}">
              <option value="daily" {{if eq $backup.Schedule "daily"}}selected{{end}}>daily</option>
              <option value="weekly" {{if eq $backup.Schedule "weekly"}}selected{{end}}>weekly</option>
            </select>
            <input type="number" name="backup_keep_count" value="{{$backup.KeepCount}}" min="0" style="width:60px;" aria-label="Backups to keep for {{$db.Database}} (0 = keep all)" title="Backups to keep (0 = keep all)">
            <label style="display:inline;margin:0;"><input type="checkbox" name="backup_restore_on_create" {{if $backup.RestoreOnCreate}}checked{{end}}> Restore on Create</label>
            <button type="submit">Save</button>
          </form>
        </td>
        <td>
          {{if ne (dbType $server.RootConnectionString) "postgres"}}&mdash;
          {{else if not $db.Migrate}}
            <form method="POST" action="/migrate-database" style="display:inline-flex;gap:0.25rem;align-items:center;flex-wrap:wrap;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <select name="target_server" aria-label="Migration target for {{$db.Database}}" required>
                <option value="">&mdash; target &mdash;</option>
                {{range $.PGServerNames}}{{if ne . $server.Name}}<option value="{{.}}">{{.}}</option>{{end}}{{end}}
              </select>
              <label style="display:inline;margin:0;"><small>To drop the source afterwards, type <code>{{$db.Database}}</code>:</small>
                <input type="text" name="confirm_database" autocomplete="off" style="width:10ch;"></label>
              <button type="submit">Migrate</button>
            </form>
          {{else if $db.Migrate.Completed}}
            <span>migrated to {{$db.Migrate.TargetServer}} at {{$db.Migrate.CompletedAt}}</span>
            {{if $db.Migrate.Error}}<br><small class="flash-err">{{$db.Migrate.Error}}</small>{{end}}
            <form method="POST" action="/migrate-database" style="display:inline;" onsubmit="return confirm('Clear the migration record for {{$db.Database}}? The provisioner will manage it on {{$server.Name}} again{{if $db.Migrate.ConfirmDrop}} and recreate it there empty, since the source was dropped{{end}}. Remove or move the entry instead if it now lives on {{$db.Migrate.TargetServer}}.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <input type="hidden" name="target_server" value="">
              <button type="submit">Clear</button>
            </form>
          {{else if $db.Migrate.Error}}
            <span class="flash-err">migration error: {{$db.Migrate.Error}}</span>
            <form method="POST" action="/migrate-database" style="display:inline;">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <input type="hidden" name="target_server" value="">
              <button type="submit">Retry (clear)</button>
            </form>
          {{else}}
            <span>migrating to {{$db.Migrate.TargetServer}}&hellip;</span>
          {{end}}
        </td>
      </tr>
      {{end}}
    </table>
    </div>
    {{end}}
  {{else}}
    <p class="empty">No servers configured yet. Add one with the Add Server form.</p>
  {{end}}

  {{if .K8sEnabled}}
  <h2>Kubernetes Secrets Mode</h2>
  <p>Passwords above are generated and stored in per-database Secrets in namespace <code>{{.Namespace}}</code>. Reference one from your own Deployment:</p>
  <pre>kubectl get secret &lt;secret-name&gt; -n {{.Namespace}} -o jsonpath='{.data.password}' | base64 -d</pre>
  <pre>env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: &lt;secret-name&gt;
      key: password</pre>
  {{end}}
  </div>

  <div class="col-right">
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

  <h2>Add Database</h2>
  {{if not .Servers}}
  <p class="empty">Add a server first, then add databases to it.</p>
  {{else}}
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
      <label>Password: <input type="password" name="password" autocomplete="new-password" required></label>
      <label>Permissions (comma-separated, blank for ALL): <input type="text" name="permissions"></label>
      <label><input type="checkbox" name="requires_connect_string"> Store full connection_string in Kubernetes secret{{if not .K8sEnabled}} (requires USE_KUBERNETES_SECRETS){{end}}</label>
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
    </fieldset>
  </form>
  {{end}}
  </div>
  </div>
</body>
</html>`))

type adminTemplateData struct {
	Servers    []DatabaseServer
	Flash      string
	FlashError bool
	K8sEnabled bool
	Namespace  string
	WatchMode  bool
	// PGServerNames lists the names of all PostgreSQL servers, offered as
	// migration targets in the admin UI.
	PGServerNames []string
}

func newAdminHandler(configPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex(configPath))
	mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
	mux.HandleFunc("/generate-password", handleGeneratePassword(configPath))
	mux.HandleFunc("/rotate-secret", handleRotateSecret(configPath))
	mux.HandleFunc("/update-backup", handleUpdateBackup(configPath))
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	mux.HandleFunc("/add-server", handleAddServer(configPath))
	mux.HandleFunc("/migrate-database", handleMigrateDatabase(configPath))
	mux.HandleFunc("GET /api/servers", handleAPIListServers(configPath))
	mux.HandleFunc("GET /api/servers/{si}", handleAPIGetServer(configPath))
	mux.HandleFunc("POST /api/servers", handleAPICreateServer(configPath))
	mux.HandleFunc("PATCH /api/servers/{si}", handleAPIUpdateServer(configPath))
	mux.HandleFunc("DELETE /api/servers/{si}", handleAPIDeleteServer(configPath))
	mux.HandleFunc("GET /api/servers/{si}/databases", handleAPIListDatabases(configPath))
	mux.HandleFunc("GET /api/servers/{si}/databases/{di}", handleAPIGetDatabase(configPath))
	mux.HandleFunc("POST /api/servers/{si}/databases", handleAPICreateDatabase(configPath))
	mux.HandleFunc("PATCH /api/servers/{si}/databases/{di}", handleAPIUpdateDatabase(configPath))
	mux.HandleFunc("DELETE /api/servers/{si}/databases/{di}", handleAPIDeleteDatabase(configPath))

	protected := basicAuth(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer favicon.ico without an auth challenge. A 401 on a path the
		// browser fetches on its own (favicon) pops a second Basic Auth
		// dialog on top of the one for "/".
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		protected.ServeHTTP(w, r)
	})
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
		adminUser := os.Getenv("ADMIN_USER")
		adminPass := os.Getenv("ADMIN_PASSWORD")
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(adminUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(adminPass)) == 1
		if !ok || !userOK || !passOK {
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
			http.Error(w, "Failed to read config file "+configPath+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			http.Error(w, "Config file "+configPath+" is not valid JSON: "+err.Error()+"\nFix the file and reload this page.", http.StatusInternalServerError)
			return
		}
		msg := r.URL.Query().Get("msg")
		tmplData := adminTemplateData{
			Servers:    cfg.Servers,
			Flash:      msg,
			FlashError: strings.HasPrefix(msg, "Error:"),
			K8sEnabled: secretsManager != nil,
			WatchMode:  os.Getenv("WATCH_MODE") == "true",
		}
		if secretsManager != nil {
			tmplData.Namespace = secretsManager.namespace
		}
		for _, s := range cfg.Servers {
			if detectDBType(s.RootConnectionString) == PostgreSQL {
				tmplData.PGServerNames = append(tmplData.PGServerNames, s.Name)
			}
		}
		if err := adminTemplate.Execute(w, tmplData); err != nil {
			log.Printf("template execute error: %v", err)
		}
	}
}

func handleUpdatePassword(configPath string) http.HandlerFunc {
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
		newPassword := r.FormValue("new_password")
		if newPassword == "" {
			http.Error(w, "new_password is required", http.StatusBadRequest)
			return
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
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Password updated"), http.StatusSeeOther)
	}
}

func handleMigrateDatabase(configPath string) http.HandlerFunc {
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
		targetServer := strings.TrimSpace(r.FormValue("target_server"))
		confirmName := strings.TrimSpace(r.FormValue("confirm_database"))
		confirmDrop := r.FormValue("confirm_drop") == "on" || confirmName != ""

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
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		if targetServer == "" {
			cfg.Servers[si].Databases[di].Migrate = nil
		} else {
			if _, err := resolveTargetServer(&cfg, targetServer, cfg.Servers[si].Name); err != nil {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: "+err.Error()), http.StatusSeeOther)
				return
			}
			// Dropping the source is irreversible, so it takes the database
			// name typed back, not just a checkbox.
			if dbName := cfg.Servers[si].Databases[di].Database; confirmDrop && confirmName != dbName {
				msg := fmt.Sprintf("Error: migration not scheduled. To drop the source after migrating, type the database name %q exactly. Leave the field empty to keep the source.", dbName)
				http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
				return
			}
			cfg.Servers[si].Databases[di].Migrate = &MigrateConfig{
				TargetServer: targetServer,
				ConfirmDrop:  confirmDrop,
			}
		}

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		msg := "Migration scheduled; the source will be kept"
		if confirmDrop {
			msg = "Migration scheduled; the source will be dropped after it's verified"
		}
		if targetServer == "" {
			msg = "Migration cleared"
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

const staleRowMsg = "Error: the config file changed since this page loaded, so nothing was saved. Check the row and try again."

// staleRow reports whether the row a form was rendered for no longer sits at
// (si, di), e.g. because the config file was edited by hand after the page
// loaded. Acting on the index alone would then change a different database.
// Requests that omit the identity fields (API clients, older pages) skip it.
func staleRow(r *http.Request, cfg *Config, si, di int) bool {
	if name := r.FormValue("server"); name != "" && cfg.Servers[si].Name != name {
		return true
	}
	if db := r.FormValue("database"); db != "" && cfg.Servers[si].Databases[di].Database != db {
		return true
	}
	return false
}

func writeConfigErrMsg(err error) string {
	msg := "Error: couldn't save the config file: " + err.Error()
	if errors.Is(err, syscall.EROFS) || errors.Is(err, os.ErrPermission) {
		msg += ". The file is read-only (a Kubernetes ConfigMap mount is read-only); edit the source config instead."
	}
	return msg
}

func generatePassword() (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, 20)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}

func handleGeneratePassword(configPath string) http.HandlerFunc {
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

		newPassword, err := generatePassword()
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to generate password"), http.StatusSeeOther)
			return
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
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		dbName := cfg.Servers[si].Databases[di].Database
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		// The password is never echoed: ?msg= ends up in browser history and
		// proxy logs. It lives in the config file only.
		msg := fmt.Sprintf("Generated a new password for %s and saved it to the config file", dbName)
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func parseBackupFields(r *http.Request) BackupConfig {
	schedule := r.FormValue("backup_schedule")
	if schedule != "weekly" {
		schedule = "daily"
	}
	keepCount := 0
	if s := strings.TrimSpace(r.FormValue("backup_keep_count")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
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
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}
		cfg.Servers[si].Databases[di].Backup = &backup

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Backup settings updated"), http.StatusSeeOther)
	}
}

func handleRotateSecret(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secretsManager == nil {
			http.Error(w, "Kubernetes secrets mode is not enabled", http.StatusBadRequest)
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

		configMu.RLock()
		fileData, err := os.ReadFile(configPath)
		configMu.RUnlock()
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
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		serverName := cfg.Servers[si].Name
		dbName := cfg.Servers[si].Databases[di].Database

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if _, err := secretsManager.rotateSecret(ctx, serverName, cfg.Servers[si].RootConnectionString, cfg.Servers[si].Databases[di]); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to rotate secret: "+err.Error()), http.StatusSeeOther)
			return
		}

		// Reprovision just this one database in the background so the new
		// password actually gets applied to the DB user. This is dispatched
		// as a goroutine (rather than run synchronously) because
		// connectWithRetry can take up to ~50s worst case against an
		// unreachable host, and the HTTP handler should redirect immediately.
		singleServerConfig := &Config{
			Servers: []DatabaseServer{
				{
					Name:                 cfg.Servers[si].Name,
					RootConnectionString: cfg.Servers[si].RootConnectionString,
					DryRun:               cfg.Servers[si].DryRun,
					Databases:            []DatabaseConfig{cfg.Servers[si].Databases[di]},
				},
			},
		}
		go func() {
			if err := processConfig(singleServerConfig); err != nil {
				log.Printf("k8s-secrets: reprovision after rotate for %s/%s failed: %v", serverName, dbName, err)
			}
		}()

		msg := fmt.Sprintf("Rotated Kubernetes secret %s", secretNameFor(serverName, dbName))
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func handleAddDatabase(configPath string) http.HandlerFunc {
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

		var permissions []string
		if p := strings.TrimSpace(r.FormValue("permissions")); p != "" {
			for _, perm := range strings.Split(p, ",") {
				permissions = append(permissions, strings.TrimSpace(perm))
			}
		}

		database := strings.TrimSpace(r.FormValue("database"))
		user := strings.TrimSpace(r.FormValue("user"))
		if database == "" || user == "" {
			http.Error(w, "database and user are required", http.StatusBadRequest)
			return
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
		for _, existing := range cfg.Servers[si].Databases {
			if existing.Database == database {
				msg := fmt.Sprintf("Error: %s already has a database named %s", cfg.Servers[si].Name, database)
				http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
				return
			}
		}

		newDB := DatabaseConfig{
			Database:              database,
			User:                  user,
			Password:              r.FormValue("password"),
			Permissions:           permissions,
			RequiresConnectString: r.FormValue("requires_connect_string") == "on",
		}
		if backup := parseBackupFields(r); backup.Enabled || backup.RestoreOnCreate || backup.KeepCount != 0 {
			newDB.Backup = &backup
		}
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, newDB)

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Database added"), http.StatusSeeOther)
	}
}

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

		name := strings.TrimSpace(r.FormValue("name"))
		rootConnStr := strings.TrimSpace(r.FormValue("root_connection_string"))
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
		// Names key Kubernetes secrets and migration targets, so they must be unique.
		for _, existing := range cfg.Servers {
			if existing.Name == name {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: a server named "+name+" already exists"), http.StatusSeeOther)
				return
			}
		}
		cfg.Servers = append(cfg.Servers, newServer)

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Server added"), http.StatusSeeOther)
	}
}
