package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":       strings.Join,
	"secretName": secretNameFor,
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
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>{{if $.K8sEnabled}}Kubernetes Secret{{else}}Change Password{{end}}</th></tr>
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
      </tr>
      {{end}}
    </table>
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
	K8sEnabled bool
	Namespace  string
}

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
			http.Error(w, "Failed to read config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			http.Error(w, "Failed to parse config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		msg := r.URL.Query().Get("msg")
		tmplData := adminTemplateData{
			Servers:    cfg.Servers,
			Flash:      msg,
			FlashError: strings.HasPrefix(msg, "Error:"),
			K8sEnabled: secretsManager != nil,
		}
		if secretsManager != nil {
			tmplData.Namespace = secretsManager.namespace
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
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Password updated"), http.StatusSeeOther)
	}
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

		dbName := cfg.Servers[si].Databases[di].Database
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		msg := fmt.Sprintf("Generated password for %s: %s", dbName, newPassword)
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

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

		serverName := cfg.Servers[si].Name
		dbName := cfg.Servers[si].Databases[di].Database

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if _, err := secretsManager.rotateSecret(ctx, serverName, dbName); err != nil {
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

		database := r.FormValue("database")
		user := r.FormValue("user")
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

		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, DatabaseConfig{
			Database:    database,
			User:        user,
			Password:    r.FormValue("password"),
			Permissions: permissions,
		})

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to write config"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Database added"), http.StatusSeeOther)
	}
}
