package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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

func TestReconcilePassword_CreatesWhenMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", Password: "ignored-from-config"}
	password, err := m.reconcilePassword(context.Background(), "Main PostgreSQL", db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}
	if password == "" || password == "ignored-from-config" {
		t.Fatalf("expected a freshly generated password, got %q", password)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "main-postgresql-app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != password {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], password)
	}
}

func TestReconcilePassword_ReusesExisting(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "main-postgresql-app-db-credentials", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("existing-secret-password")},
	})
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user"}
	password, err := m.reconcilePassword(context.Background(), "Main PostgreSQL", db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}
	if password != "existing-secret-password" {
		t.Errorf("password = %q, want %q", password, "existing-secret-password")
	}
}

func TestReconcilePassword_SecretMissingPasswordKey(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "main-postgresql-app-db-credentials", Namespace: "default"},
		Data:       map[string][]byte{"other-key": []byte("x")},
	})
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db"}
	if _, err := m.reconcilePassword(context.Background(), "Main PostgreSQL", db); err == nil {
		t.Fatal("expected error when secret has no password key")
	}
}
