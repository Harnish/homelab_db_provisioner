package main

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestApplyK8sPassword_MultipleDatabasesGetDistinctSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	dbs := []DatabaseConfig{
		{Database: "app_db", Password: "config-a"},
		{Database: "analytics_db", Password: "config-b"},
	}

	var resolved []DatabaseConfig
	for _, db := range dbs {
		got, err := applyK8sPassword(context.Background(), "Main PostgreSQL", db)
		if err != nil {
			t.Fatalf("applyK8sPassword(%s) error = %v", db.Database, err)
		}
		resolved = append(resolved, got)
	}

	if resolved[0].Password == resolved[1].Password {
		t.Fatalf("expected distinct passwords per database, both got %q", resolved[0].Password)
	}
	if resolved[0].Password == "config-a" || resolved[1].Password == "config-b" {
		t.Fatal("expected config.json passwords to be overridden, not passed through")
	}
}
