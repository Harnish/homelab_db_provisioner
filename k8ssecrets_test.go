package main

import "testing"

func TestSecretNameFor(t *testing.T) {
	cases := []struct {
		serverName string
		database   string
		want       string
	}{
		{"Main PostgreSQL", "app_db", "main-postgresql-app-db-credentials"},
		{"Production MariaDB", "wordpress_db", "production-mariadb-wordpress-db-credentials"},
	}
	for _, c := range cases {
		got := secretNameFor(c.serverName, c.database)
		if got != c.want {
			t.Errorf("secretNameFor(%q, %q) = %q, want %q", c.serverName, c.database, got, c.want)
		}
	}
}
