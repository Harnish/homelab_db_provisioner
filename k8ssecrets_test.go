package main

import (
	"bytes"
	"context"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testRootConn = "postgres://root:root@localhost:5432/postgres"

// managedTestSecret is a Secret as this provisioner would have created it.
func managedTestSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{k8sManagedByLabel: k8sManagedByValue},
		},
		Data: data,
	}
}

func TestDefaultSecretName(t *testing.T) {
	if got := defaultSecretName("app_db"); got != "app-db-credentials" {
		t.Errorf("defaultSecretName(app_db) = %q, want app-db-credentials", got)
	}
}

func TestClaimSecretName(t *testing.T) {
	cfg := &Config{Servers: []DatabaseServer{
		{Name: "old", Databases: []DatabaseConfig{
			{Database: "gone", K8sSecret: "gone-credentials", Migrate: &MigrateConfig{Completed: true}},
		}},
		{Name: "pg", Databases: []DatabaseConfig{
			{Database: "app", K8sSecret: "app-credentials"},
		}},
	}}
	if got, err := claimSecretName(cfg, "", "wiki_db"); err != nil || got != "wiki-db-credentials" {
		t.Errorf("blank name: got %q, %v; want the default", got, err)
	}
	if got, err := claimSecretName(cfg, "  custom-name ", "x"); err != nil || got != "custom-name" {
		t.Errorf("custom name: got %q, %v", got, err)
	}
	if _, err := claimSecretName(cfg, "", "app"); err == nil {
		t.Error("expected error: app-credentials is already used by pg/app")
	}
	if _, err := claimSecretName(cfg, "gone-credentials", "gone"); err != nil {
		t.Errorf("a migrated-away tombstone shouldn't block its own Secret name: %v", err)
	}
	if _, err := claimSecretName(cfg, "Not_Valid", "x"); err == nil {
		t.Error("expected error for a name that isn't a valid Kubernetes name")
	}
}

func TestReconcilePassword_CreatesWhenMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", K8sSecret: "app-db-credentials"}
	password, err := m.reconcilePassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}
	if password == "" {
		t.Fatal("expected a freshly generated password")
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != password {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], password)
	}
	if secret.Labels[k8sManagedByLabel] != k8sManagedByValue {
		t.Errorf("expected managed-by label, got %v", secret.Labels)
	}
}

func TestReconcilePassword_SeedsFromConfigPassword(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", Password: "from-config", K8sSecret: "app-db-credentials"}
	password, err := m.reconcilePassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}
	if password != "from-config" {
		t.Fatalf("password = %q, want the config password kept on move", password)
	}
	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != "from-config" {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], "from-config")
	}
}

func TestReconcilePassword_CreatesWithConnectionStringWhenRequired(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", RequiresConnectString: true, K8sSecret: "app-db-credentials"}
	password, err := m.reconcilePassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	// generatePassword's charset includes reserved URL characters (@ # % &),
	// so compare structurally via url.Parse rather than raw string equality
	// (a percent-encoded password won't string-match the raw one).
	got, err := url.Parse(string(secret.Data["connection_string"]))
	if err != nil {
		t.Fatalf("connection_string is not a valid URL: %v", err)
	}
	if got.Scheme != "postgres" || got.Host != "localhost:5432" || got.Path != "/app_db" {
		t.Errorf("connection_string = %q, want scheme=postgres host=localhost:5432 path=/app_db", got)
	}
	if got.User.Username() != "app_user" {
		t.Errorf("connection_string user = %q, want %q", got.User.Username(), "app_user")
	}
	if pw, _ := got.User.Password(); pw != password {
		t.Errorf("connection_string password = %q, want %q", pw, password)
	}
}

func TestReconcilePassword_BackfillsConnectionStringOnExisting(t *testing.T) {
	client := fake.NewSimpleClientset(managedTestSecret("app-db-credentials", map[string][]byte{"password": []byte("existing-secret-password")}))
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", RequiresConnectString: true, K8sSecret: "app-db-credentials"}
	if _, err := m.reconcilePassword(context.Background(), testRootConn, db); err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	want := "postgres://app_user:existing-secret-password@localhost:5432/app_db"
	if string(secret.Data["connection_string"]) != want {
		t.Errorf("connection_string = %q, want %q", secret.Data["connection_string"], want)
	}
}

func TestReconcilePassword_ReusesExisting(t *testing.T) {
	client := fake.NewSimpleClientset(managedTestSecret("app-db-credentials", map[string][]byte{"password": []byte("existing-secret-password")}))
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", User: "app_user", K8sSecret: "app-db-credentials"}
	password, err := m.reconcilePassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("reconcilePassword() error = %v", err)
	}
	if password != "existing-secret-password" {
		t.Errorf("password = %q, want %q", password, "existing-secret-password")
	}
}

func TestReconcilePassword_RefusesUnmanagedSecret(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("someone-elses")},
	})
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app", K8sSecret: "app-credentials"}
	if _, err := m.reconcilePassword(context.Background(), testRootConn, db); err == nil {
		t.Fatal("expected error instead of adopting a Secret the provisioner didn't create")
	}
}

func TestReconcilePassword_SecretMissingPasswordKey(t *testing.T) {
	client := fake.NewSimpleClientset(managedTestSecret("app-db-credentials", map[string][]byte{"other-key": []byte("x")}))
	m := &k8sSecretsManager{client: client, namespace: "default"}

	db := DatabaseConfig{Database: "app_db", K8sSecret: "app-db-credentials"}
	if _, err := m.reconcilePassword(context.Background(), testRootConn, db); err == nil {
		t.Fatal("expected error when secret has no password key")
	}
}

func TestApplyK8sPassword_NoManagerReturnsUnchanged(t *testing.T) {
	secretsManager = nil
	db := DatabaseConfig{Database: "app_db", Password: "from-config"}
	got, err := applyK8sPassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("applyK8sPassword() error = %v", err)
	}
	if got.Password != "from-config" {
		t.Errorf("password = %q, want unchanged %q", got.Password, "from-config")
	}
}

func TestApplyK8sPassword_NotInSecretNeverTouchesKubernetes(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	db := DatabaseConfig{Database: "app_db", Password: "from-config"}
	got, err := applyK8sPassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("applyK8sPassword() error = %v", err)
	}
	if got.Password != "from-config" {
		t.Errorf("password = %q, want config password", got.Password)
	}
	if n := len(client.Actions()); n != 0 {
		t.Errorf("expected no Kubernetes API calls, got %d: %v", n, client.Actions())
	}
}

func TestApplyK8sPassword_FillsFromSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	db := DatabaseConfig{Database: "app_db", K8sSecret: "app-db-credentials"}
	got, err := applyK8sPassword(context.Background(), testRootConn, db)
	if err != nil {
		t.Fatalf("applyK8sPassword() error = %v", err)
	}
	if got.Password == "" {
		t.Error("expected password filled from the generated secret")
	}
}

func TestApplyK8sPassword_InSecretWithoutManagerErrors(t *testing.T) {
	secretsManager = nil
	db := DatabaseConfig{Database: "app_db", K8sSecret: "app-db-credentials"}
	if _, err := applyK8sPassword(context.Background(), testRootConn, db); err == nil {
		t.Fatal("expected error instead of provisioning with an empty password")
	}
}

func TestApplyK8sPassword_PropagatesError(t *testing.T) {
	client := fake.NewSimpleClientset(managedTestSecret("app-db-credentials", map[string][]byte{"other-key": []byte("x")}))
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	db := DatabaseConfig{Database: "app_db", K8sSecret: "app-db-credentials"}
	if _, err := applyK8sPassword(context.Background(), testRootConn, db); err == nil {
		t.Fatal("expected error to propagate from reconcilePassword")
	}
}

func TestRotateSecret_UpdatesExisting(t *testing.T) {
	client := fake.NewSimpleClientset(managedTestSecret("app-db-credentials", map[string][]byte{"password": []byte("old-password")}))
	m := &k8sSecretsManager{client: client, namespace: "default"}

	newPassword, err := m.rotateSecret(context.Background(), testRootConn, DatabaseConfig{Database: "app_db", User: "app_user", K8sSecret: "app-db-credentials"})
	if err != nil {
		t.Fatalf("rotateSecret() error = %v", err)
	}
	if newPassword == "old-password" || newPassword == "" {
		t.Fatalf("expected a new non-empty password, got %q", newPassword)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(secret.Data["password"]) != newPassword {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], newPassword)
	}
}

func TestRotateSecret_CreatesWhenMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m := &k8sSecretsManager{client: client, namespace: "default"}

	password, err := m.rotateSecret(context.Background(), testRootConn, DatabaseConfig{Database: "app_db", User: "app_user", K8sSecret: "app-db-credentials"})
	if err != nil {
		t.Fatalf("rotateSecret() error = %v", err)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != password {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], password)
	}
}

func TestRotateSecret_RequiresSecretName(t *testing.T) {
	m := &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	if _, err := m.rotateSecret(context.Background(), testRootConn, DatabaseConfig{Database: "app_db"}); err == nil {
		t.Fatal("expected error for a database whose password isn't in a Secret")
	}
}

// TestProcessConfig_DryRunSkipsK8sSecretReconciliation exercises the
// dry-run gate added around applyK8sPassword in processConfig's MongoDB
// loop. There's no real Mongo/Postgres/MariaDB available in tests (an
// existing, accepted limitation of this codebase), so this uses the
// MongoDB path with a deliberately host-less "mongodb://" connection
// string: mongo.Connect fails synchronously on URI parsing ("must have
// at least 1 host") without any network I/O, so the test stays fast and
// hermetic while still driving processConfig's real per-database loop.
//
// Before the fix, applyK8sPassword ran unconditionally before the
// server.DryRun-gated provisioning call, so a dry-run server would still
// create a real Kubernetes Secret. This test would fail against that
// prior behavior (the fake clientset would happily create the secret
// regardless of Mongo connectivity) and passes with the fix, which skips
// the k8s reconciliation entirely when server.DryRun is true.
func TestProcessConfig_DryRunSkipsK8sSecretReconciliation(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	cfg := &Config{
		Servers: []DatabaseServer{
			{
				Name:                 "Dry Run Mongo",
				RootConnectionString: "mongodb://",
				DryRun:               true,
				Databases: []DatabaseConfig{
					{Database: "app_db", User: "app_user", K8sSecret: "app-db-credentials"},
				},
			},
		},
	}

	if err := processConfig(cfg); err != nil {
		t.Fatalf("processConfig() error = %v", err)
	}

	if _, err := client.CoreV1().Secrets("default").Get(context.Background(), "app-db-credentials", metav1.GetOptions{}); err == nil {
		t.Fatal("expected no Kubernetes secret to be created in dry-run mode")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error checking for secret: %v", err)
	}
}

// TestProcessConfig_DryRunLogsRequiresConnectString checks that dry-run mode
// logs the intent to store a connection_string key when
// DatabaseConfig.RequiresConnectString is set, without actually reconciling
// a Kubernetes secret (dry-run still skips applyK8sPassword entirely).
func TestProcessConfig_DryRunLogsRequiresConnectString(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	cfg := &Config{
		Servers: []DatabaseServer{
			{
				Name:                 "Dry Run Mongo",
				RootConnectionString: "mongodb://",
				DryRun:               true,
				Databases: []DatabaseConfig{
					{Database: "app_db", User: "app_user", RequiresConnectString: true, K8sSecret: "app-db-credentials"},
				},
			},
		},
	}

	if err := processConfig(cfg); err != nil {
		t.Fatalf("processConfig() error = %v", err)
	}

	want := "Would store connection_string in Kubernetes secret app-db-credentials"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("expected dry-run log to contain %q, got: %s", want, buf.String())
	}
}

func TestReadNamespaceFile_Success(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte("my-namespace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ns, err := readNamespaceFile(path)
	if err != nil {
		t.Fatalf("readNamespaceFile() error = %v", err)
	}
	if ns != "my-namespace" {
		t.Errorf("namespace = %q, want %q", ns, "my-namespace")
	}
}

func TestReadNamespaceFile_MissingFile(t *testing.T) {
	if _, err := readNamespaceFile(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadNamespaceFile_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte("   \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNamespaceFile(path); err == nil {
		t.Fatal("expected error for empty/whitespace-only namespace file")
	}
}
