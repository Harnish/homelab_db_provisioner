package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
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
	return os.WriteFile(configPath, out, 0600)
}

type databaseResponse struct {
	Index       int           `json:"index"`
	Database    string        `json:"database"`
	User        string        `json:"user"`
	Permissions []string      `json:"permissions,omitempty"`
	Extensions  []string      `json:"extensions,omitempty"`
	Backup      *BackupConfig `json:"backup,omitempty"`
}

func toDatabaseResponse(di int, d DatabaseConfig) databaseResponse {
	return databaseResponse{
		Index:       di,
		Database:    d.Database,
		User:        d.User,
		Permissions: d.Permissions,
		Extensions:  d.Extensions,
		Backup:      d.Backup,
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
		RootConnectionString: s.RootConnectionString,
		DryRun:               s.DryRun,
		Databases:            dbs,
	}
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
		if req.Database == "" || req.User == "" || req.Password == "" {
			writeJSONError(w, http.StatusBadRequest, "database, user, and password are required")
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

		newDB := DatabaseConfig{
			Database:    req.Database,
			User:        req.User,
			Password:    req.Password,
			Permissions: req.Permissions,
			Extensions:  req.Extensions,
			Backup:      req.Backup,
		}
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, newDB)
		di := len(cfg.Servers[si].Databases) - 1

		if err := saveConfigFile(configPath, cfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to write config: "+err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toDatabaseResponse(di, newDB))
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
