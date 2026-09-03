package main

import (
	"fmt"
	"sort"
	"strings"
)

func resolveTargetServer(config *Config, targetName, sourceName string) (DatabaseServer, error) {
	if targetName == sourceName {
		return DatabaseServer{}, fmt.Errorf("target server must differ from source")
	}
	for _, s := range config.Servers {
		if s.Name != targetName {
			continue
		}
		if detectDBType(s.RootConnectionString) != PostgreSQL {
			return DatabaseServer{}, fmt.Errorf("target server %q is not PostgreSQL", targetName)
		}
		return s, nil
	}
	return DatabaseServer{}, fmt.Errorf("target server %q not found", targetName)
}

var connErrMarkers = []string{
	"could not connect",
	"connection refused",
	"could not translate host name",
	"no such host",
	"connection timed out",
}

func isConnectionError(stderr string) bool {
	low := strings.ToLower(stderr)
	for _, m := range connErrMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// compareTableCounts returns nil only if both maps have identical keys and values.
func compareTableCounts(source, target map[string]int64) error {
	keys := make([]string, 0, len(source))
	for k := range source {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tv, ok := target[k]
		if !ok {
			return fmt.Errorf("table %s present on source but missing on target", k)
		}
		if tv != source[k] {
			return fmt.Errorf("table %s row count mismatch: source=%d target=%d", k, source[k], tv)
		}
	}
	extra := make([]string, 0)
	for k := range target {
		if _, ok := source[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("tables present on target but missing on source: %s", strings.Join(extra, ", "))
	}
	return nil
}
