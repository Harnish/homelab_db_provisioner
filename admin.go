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
