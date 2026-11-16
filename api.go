package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func loadConfigFile(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfigFile(configPath string, cfg *Config) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(configPath, out)
}

// writeFileAtomic replaces path with data via a temp file and rename, so a
// crash or full disk mid-write can't leave the config (which holds every
// password) empty or truncated. Where a rename isn't possible (a single-file
// bind mount, a read-only directory) it writes in place as before.
func writeFileAtomic(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved // replace the file a symlink points at, not the link
	}
	mode := os.FileMode(0600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return os.WriteFile(path, data, 0600)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return os.WriteFile(path, data, 0600)
	}
	return nil
}

type databaseResponse struct {
	Index       int           `json:"index"`
	Database    string        `json:"database"`
	User        string        `json:"user"`
	Permissions []string      `json:"permissions,omitempty"`
	Extensions  []string      `json:"extensions,omitempty"`
	Backup      *BackupConfig `json:"backup,omitempty"`
	K8sSecret   string        `json:"k8s_secret,omitempty"`
}

func toDatabaseResponse(di int, d DatabaseConfig) databaseResponse {
	return databaseResponse{
		Index:       di,
		Database:    d.Database,
		User:        d.User,
		Permissions: d.Permissions,
		Extensions:  d.Extensions,
		Backup:      d.Backup,
		K8sSecret:   d.K8sSecret,
	}
}

type serverResponse struct {
	Index                int                `json:"index"`
	Name                 string             `json:"name"`
	RootConnectionString string             `json:"root_connection_string"`
	DryRun               bool               `json:"dry_run"`
	Databases            []databaseResponse `json:"databases"`
}

func toServerResponse(si int, s DatabaseServer) serverResponse {
	dbs := make([]databaseResponse, len(s.Databases))
	for di, d := range s.Databases {
		dbs[di] = toDatabaseResponse(di, d)
	}
	return serverResponse{
		Index:                si,
		Name:                 s.Name,
		RootConnectionString: redactConnString(s.RootConnectionString),
		DryRun:               s.DryRun,
		Databases:            dbs,
	}
}

const redactedPassword = "REDACTED"

// redactConnString hides the password in a connection URL for API responses.
// It cuts from the first ":" after the scheme to the last "@", so a password
// with unusual characters is over-redacted rather than leaked.
func redactConnString(s string) string {
	i := strings.Index(s, "://")
	if i < 0 {
		return s
	}
	rest := s[i+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return s
	}
	colon := strings.Index(rest[:at], ":")
	if colon < 0 {
		return s
	}
	return s[:i+3] + rest[:colon+1] + redactedPassword + rest[at:]
}

// checkServer validates a server being created or updated at index skip
// (-1 for a new one). Names are looked up by migrations and slugged into
// backup directory names, so they must be unique after slugging too.
func checkServer(cfg *Config, name, rootConnStr string, skip int) error {
	if slugify(name) == "" {
		return fmt.Errorf("server name must contain a letter or digit")
	}
	// A client that reads a server and writes it back would otherwise
	// replace the real password with the placeholder.
	if strings.Contains(rootConnStr, ":"+redactedPassword+"@") {
		return fmt.Errorf("root_connection_string contains the %s placeholder; send the real password", redactedPassword)
	}
	for i, s := range cfg.Servers {
		if i != skip && slugify(s.Name) == slugify(name) {
			return fmt.Errorf("a server named %q already exists", s.Name)
		}
	}
	return nil
}

// checkDatabase validates db for server and rejects a second entry with the
// same database name (skip is the index being updated, -1 for a new one).
func checkDatabase(server DatabaseServer, db DatabaseConfig, skip int) error {
	if err := validateDatabaseConfig(detectDBType(server.RootConnectionString), db); err != nil {
		return err
	}
	for i, d := range server.Databases {
		if i != skip && d.Database == db.Database {
			return fmt.Errorf("%s already has a database named %s", server.Name, db.Database)
		}
	}
	return nil
}

func handleAPIListServers(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configMu.RLock()
		cfg, err := loadConfigFile(configPath)
		configMu.RUnlock()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		servers := make([]serverResponse, len(cfg.Servers))
		for si, s := range cfg.Servers {
			servers[si] = toServerResponse(si, s)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"servers": servers})
	}
}

func handleAPIGetServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		configMu.RLock()
		cfg, err := loadConfigFile(configPath)
		configMu.RUnlock()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		writeJSON(w, http.StatusOK, toServerResponse(si, cfg.Servers[si]))
	}
}

type createServerRequest struct {
	Name                 string `json:"name"`
	RootConnectionString string `json:"root_connection_string"`
	DryRun               bool   `json:"dry_run"`
}

func handleAPICreateServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createServerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Name == "" || req.RootConnectionString == "" {
			writeJSONError(w, http.StatusBadRequest, "name and root_connection_string are required")
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if err := checkServer(cfg, req.Name, req.RootConnectionString, -1); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		newServer := DatabaseServer{
			Name:                 req.Name,
			RootConnectionString: req.RootConnectionString,
			DryRun:               req.DryRun,
			Databases:            []DatabaseConfig{},
		}
		cfg.Servers = append(cfg.Servers, newServer)
		si := len(cfg.Servers) - 1

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toServerResponse(si, newServer))
	}
}

type updateServerRequest struct {
	Name                 *string `json:"name"`
	RootConnectionString *string `json:"root_connection_string"`
	DryRun               *bool   `json:"dry_run"`
}

func handleAPIUpdateServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		var req updateServerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}

		if req.Name != nil {
			cfg.Servers[si].Name = *req.Name
		}
		if req.RootConnectionString != nil {
			cfg.Servers[si].RootConnectionString = *req.RootConnectionString
		}
		if req.DryRun != nil {
			cfg.Servers[si].DryRun = *req.DryRun
		}
		if req.RootConnectionString != nil && *req.RootConnectionString == "" {
			writeJSONError(w, http.StatusBadRequest, "root_connection_string can't be empty")
			return
		}
		if req.Name != nil || req.RootConnectionString != nil {
			if err := checkServer(cfg, cfg.Servers[si].Name, cfg.Servers[si].RootConnectionString, si); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toServerResponse(si, cfg.Servers[si]))
	}
}

func handleAPIDeleteServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		cfg.Servers = append(cfg.Servers[:si], cfg.Servers[si+1:]...)

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAPIListDatabases(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		configMu.RLock()
		cfg, err := loadConfigFile(configPath)
		configMu.RUnlock()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		dbs := make([]databaseResponse, len(cfg.Servers[si].Databases))
		for di, d := range cfg.Servers[si].Databases {
			dbs[di] = toDatabaseResponse(di, d)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"databases": dbs})
	}
}

func handleAPIGetDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		di, err := strconv.Atoi(r.PathValue("di"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid database index")
			return
		}
		configMu.RLock()
		cfg, err := loadConfigFile(configPath)
		configMu.RUnlock()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			writeJSONError(w, http.StatusNotFound, "database not found")
			return
		}
		writeJSON(w, http.StatusOK, toDatabaseResponse(di, cfg.Servers[si].Databases[di]))
	}
}

type createDatabaseRequest struct {
	Database    string        `json:"database"`
	User        string        `json:"user"`
	Password    string        `json:"password"`
	Permissions []string      `json:"permissions"`
	Extensions  []string      `json:"extensions"`
	Backup      *BackupConfig `json:"backup"`
	SecretName  string        `json:"secret_name"` // Kubernetes Secret mode only; blank for <database>-credentials
}

func handleAPICreateDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		var req createDatabaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Database == "" || req.User == "" {
			writeJSONError(w, http.StatusBadRequest, "database and user are required")
			return
		}
		// In Kubernetes Secret mode new databases get a generated password in
		// a Secret, so there is nothing to supply; otherwise it's required.
		if secretsManager != nil && req.Password != "" {
			writeJSONError(w, http.StatusBadRequest, "omit password: new databases get a generated password in a Kubernetes Secret")
			return
		}
		// Blank password outside k8s mode: generate one and return it once in
		// the create response, since GET never returns passwords.
		var generated string
		if secretsManager == nil && req.Password == "" {
			if generated, err = generatePassword(); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to generate password")
				return
			}
			req.Password = generated
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}

		newDB := DatabaseConfig{
			Database:    req.Database,
			User:        req.User,
			Password:    req.Password,
			Permissions: req.Permissions,
			Extensions:  req.Extensions,
			Backup:      req.Backup,
		}
		if err := checkDatabase(cfg.Servers[si], newDB, -1); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if secretsManager != nil {
			if newDB.K8sSecret, err = claimSecretName(cfg, req.SecretName, req.Database); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, newDB)
		di := len(cfg.Servers[si].Databases) - 1

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, struct {
			databaseResponse
			GeneratedPassword string `json:"generated_password,omitempty"`
		}{toDatabaseResponse(di, newDB), generated})
	}
}

type updateDatabaseRequest struct {
	User        *string       `json:"user"`
	Password    *string       `json:"password"`
	Permissions *[]string     `json:"permissions"`
	Extensions  *[]string     `json:"extensions"`
	Backup      *BackupConfig `json:"backup"`
}

func handleAPIUpdateDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		di, err := strconv.Atoi(r.PathValue("di"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid database index")
			return
		}
		var req updateDatabaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			writeJSONError(w, http.StatusNotFound, "database not found")
			return
		}

		db := &cfg.Servers[si].Databases[di]
		if req.User != nil {
			db.User = *req.User
		}
		if req.Password != nil {
			db.Password = *req.Password
		}
		if req.Permissions != nil {
			db.Permissions = *req.Permissions
		}
		if req.Extensions != nil {
			db.Extensions = *req.Extensions
		}
		if req.Backup != nil {
			db.Backup = req.Backup
		}
		if err := checkDatabase(cfg.Servers[si], *db, di); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toDatabaseResponse(di, *db))
	}
}

func handleAPIDeleteDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si, err := strconv.Atoi(r.PathValue("si"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid server index")
			return
		}
		di, err := strconv.Atoi(r.PathValue("di"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid database index")
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		cfg, err := loadConfigFile(configPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read config: "+err.Error())
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			writeJSONError(w, http.StatusNotFound, "server not found")
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			writeJSONError(w, http.StatusNotFound, "database not found")
			return
		}
		dbs := cfg.Servers[si].Databases
		cfg.Servers[si].Databases = append(dbs[:di], dbs[di+1:]...)

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
