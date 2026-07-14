# Kubernetes Secrets for Generated DB Passwords Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in `USE_KUBERNETES_SECRETS` env var that, when `true`, makes the provisioner ignore each database's `password` field in `config.json` and instead get-or-create a per-database Kubernetes `Secret` holding a generated password, using that password to provision the DB user. Admin UI surfaces the secret name and a rotate button plus usage instructions.

**Architecture:** New file `k8ssecrets.go` wraps `k8s.io/client-go` behind a `k8sSecretsManager` struct whose `client` field is the `kubernetes.Interface` (so tests inject a fake clientset). A package-level `secretsManager *k8sSecretsManager` is `nil` unless the env var is set, initialized once in `main()` via in-cluster config (fail-fast on error). `processConfig()` calls a small wrapper (`applyK8sPassword`) before each provisioning call to override `DatabaseConfig.Password` in memory only — `config.json` is never read for, or written with, the real password in this mode. Admin UI (`admin.go`) conditionally swaps the password-entry column for a secret name + Rotate button when `secretsManager != nil`.

**Tech Stack:** Go 1.26 (existing), `k8s.io/client-go` + `k8s.io/apimachinery` (new), `k8s.io/client-go/kubernetes/fake` (test-only).

## Global Constraints

- Only works inside a Kubernetes pod (in-cluster config). No kubeconfig fallback. Fail fast (`log.Fatal`) at startup if in-cluster config can't be built when `USE_KUBERNETES_SECRETS=true`.
- Secret content: single key `password` only.
- Secret name: `<slugify(server.Name)>-<slugify(db.Database)>-credentials`, reusing `slugify()` from `backup.go`.
- Namespace read from `/var/run/secrets/kubernetes.io/serviceaccount/namespace` — no new namespace env var.
- Existing secret found → reuse its password verbatim, never mutate it during normal reconciliation (only the admin UI Rotate button mutates it).
- `config.json`'s `password` field is never read or written by this feature — leave it exactly as-is on disk.
- Backup/restore code paths are untouched (they authenticate via the server root connection string, not `DatabaseConfig.Password`).
- Per-database k8s API failure during reconciliation → log and skip that database, continue with the rest (same pattern as existing provisioning error handling in `processConfig`).
- `go test ./...` must pass unmodified (no network/cluster access) at every commit in this plan.

---

### Task 1: Secret naming helper + client-go dependency

**Files:**
- Create: `k8ssecrets.go`
- Test: `k8ssecrets_test.go`
- Modify: `go.mod`, `go.sum` (via `go get`)

**Interfaces:**
- Produces: `func secretNameFor(serverName, database string) string` — used by Task 2 (reconcile), Task 3 (rotate), Task 6 (admin template).

- [ ] **Step 1: Add the client-go dependency**

Run:
```bash
cd /home/jharnish/Work/homelab_db_provisioner
go get k8s.io/client-go@v0.31.3
go get k8s.io/apimachinery@v0.31.3
```
Expected: `go.mod` gains `k8s.io/client-go` and `k8s.io/apimachinery` require lines; `go.sum` gains many new entries. No errors.

- [ ] **Step 2: Write the failing test for `secretNameFor`**

Create `k8ssecrets_test.go`:

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./... -run TestSecretNameFor -v`
Expected: FAIL — `undefined: secretNameFor` (compile error, since `k8ssecrets.go` doesn't define it yet).

- [ ] **Step 4: Create `k8ssecrets.go` with the helper**

```go
package main

import "fmt"

func secretNameFor(serverName, database string) string {
	return fmt.Sprintf("%s-%s-credentials", slugify(serverName), slugify(database))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./... -run TestSecretNameFor -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum k8ssecrets.go k8ssecrets_test.go
git commit -m "feat: add k8ssecrets.go with secret naming helper and client-go dependency"
```

---

### Task 2: `k8sSecretsManager.reconcilePassword` (get-or-create)

**Files:**
- Modify: `k8ssecrets.go`
- Test: `k8ssecrets_test.go`

**Interfaces:**
- Consumes: `secretNameFor(serverName, database string) string` (Task 1), `generatePassword() (string, error)` (exists in `admin.go`), `DatabaseConfig` (exists in `main.go`).
- Produces:
  - `type k8sSecretsManager struct { client kubernetes.Interface; namespace string }`
  - `func (m *k8sSecretsManager) reconcilePassword(ctx context.Context, serverName string, db DatabaseConfig) (string, error)` — used by Task 3's `applyK8sPassword` and Task 5's `processConfig` integration.
  - `var secretsManager *k8sSecretsManager` (package-level, nil by default) — used by Task 3, Task 5, Task 6.

- [ ] **Step 1: Write the failing tests**

Add to `k8ssecrets_test.go`:

```go
import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestReconcilePassword -v`
Expected: FAIL — `undefined: k8sSecretsManager` (compile error).

- [ ] **Step 3: Implement `k8sSecretsManager` and `reconcilePassword`**

Append to `k8ssecrets.go` (also add the new imports):

```go
import (
	"context"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// k8sSecretsManager reconciles per-database passwords against Kubernetes Secrets.
// client is kubernetes.Interface (not *kubernetes.Clientset) so tests can inject a fake clientset.
type k8sSecretsManager struct {
	client    kubernetes.Interface
	namespace string
}

// secretsManager is nil unless USE_KUBERNETES_SECRETS=true.
var secretsManager *k8sSecretsManager

const k8sManagedByLabel = "app.kubernetes.io/managed-by"

func (m *k8sSecretsManager) reconcilePassword(ctx context.Context, serverName string, db DatabaseConfig) (string, error) {
	name := secretNameFor(serverName, db.Database)

	secret, err := m.client.CoreV1().Secrets(m.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		pw, ok := secret.Data["password"]
		if !ok || len(pw) == 0 {
			return "", fmt.Errorf("secret %s exists but has no password key", name)
		}
		return string(pw), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get secret %s: %w", name, err)
	}

	password, err := generatePassword()
	if err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.namespace,
			Labels:    map[string]string{k8sManagedByLabel: "homelab-db-provisioner"},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"password": password},
	}
	if _, err := m.client.CoreV1().Secrets(m.namespace).Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create secret %s: %w", name, err)
	}
	log.Printf("k8s-secrets: created secret %s", name)
	return password, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestReconcilePassword -v`
Expected: PASS (all three subtests)

- [ ] **Step 5: Commit**

```bash
git add k8ssecrets.go k8ssecrets_test.go
git commit -m "feat: add k8sSecretsManager.reconcilePassword get-or-create logic"
```

---

### Task 3: `applyK8sPassword` wrapper + `rotateSecret`

**Files:**
- Modify: `k8ssecrets.go`
- Test: `k8ssecrets_test.go`

**Interfaces:**
- Consumes: `secretsManager` (Task 2), `(*k8sSecretsManager).reconcilePassword` (Task 2), `secretNameFor` (Task 1).
- Produces:
  - `func applyK8sPassword(ctx context.Context, serverName string, db DatabaseConfig) (DatabaseConfig, error)` — used by Task 5's `processConfig` integration.
  - `func (m *k8sSecretsManager) rotateSecret(ctx context.Context, serverName, database string) (string, error)` — used by Task 6's admin rotate handler.

- [ ] **Step 1: Write the failing tests**

Add to `k8ssecrets_test.go`:

```go
func TestApplyK8sPassword_NoManagerReturnsUnchanged(t *testing.T) {
	secretsManager = nil
	db := DatabaseConfig{Database: "app_db", Password: "from-config"}
	got, err := applyK8sPassword(context.Background(), "Main PostgreSQL", db)
	if err != nil {
		t.Fatalf("applyK8sPassword() error = %v", err)
	}
	if got.Password != "from-config" {
		t.Errorf("password = %q, want unchanged %q", got.Password, "from-config")
	}
}

func TestApplyK8sPassword_OverridesFromSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	db := DatabaseConfig{Database: "app_db", Password: "from-config"}
	got, err := applyK8sPassword(context.Background(), "Main PostgreSQL", db)
	if err != nil {
		t.Fatalf("applyK8sPassword() error = %v", err)
	}
	if got.Password == "from-config" || got.Password == "" {
		t.Errorf("expected password overridden with generated secret value, got %q", got.Password)
	}
}

func TestApplyK8sPassword_PropagatesError(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "main-postgresql-app-db-credentials", Namespace: "default"},
		Data:       map[string][]byte{"other-key": []byte("x")},
	})
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	db := DatabaseConfig{Database: "app_db", Password: "from-config"}
	_, err := applyK8sPassword(context.Background(), "Main PostgreSQL", db)
	if err == nil {
		t.Fatal("expected error to propagate from reconcilePassword")
	}
}

func TestRotateSecret_UpdatesExisting(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "main-postgresql-app-db-credentials", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("old-password")},
	})
	m := &k8sSecretsManager{client: client, namespace: "default"}

	newPassword, err := m.rotateSecret(context.Background(), "Main PostgreSQL", "app_db")
	if err != nil {
		t.Fatalf("rotateSecret() error = %v", err)
	}
	if newPassword == "old-password" || newPassword == "" {
		t.Fatalf("expected a new non-empty password, got %q", newPassword)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "main-postgresql-app-db-credentials", metav1.GetOptions{})
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

	password, err := m.rotateSecret(context.Background(), "Main PostgreSQL", "app_db")
	if err != nil {
		t.Fatalf("rotateSecret() error = %v", err)
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), "main-postgresql-app-db-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}
	if string(secret.Data["password"]) != password {
		t.Errorf("secret password = %q, want %q", secret.Data["password"], password)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestApplyK8sPassword|TestRotateSecret' -v`
Expected: FAIL — `undefined: applyK8sPassword` / `undefined: (*k8sSecretsManager).rotateSecret` (compile error).

- [ ] **Step 3: Implement `applyK8sPassword` and `rotateSecret`**

Append to `k8ssecrets.go`:

```go
func applyK8sPassword(ctx context.Context, serverName string, db DatabaseConfig) (DatabaseConfig, error) {
	if secretsManager == nil {
		return db, nil
	}
	password, err := secretsManager.reconcilePassword(ctx, serverName, db)
	if err != nil {
		return db, err
	}
	db.Password = password
	return db, nil
}

func (m *k8sSecretsManager) rotateSecret(ctx context.Context, serverName, database string) (string, error) {
	name := secretNameFor(serverName, database)
	password, err := generatePassword()
	if err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}

	secret, err := m.client.CoreV1().Secrets(m.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get secret %s: %w", name, err)
		}
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: m.namespace,
				Labels:    map[string]string{k8sManagedByLabel: "homelab-db-provisioner"},
			},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{"password": password},
		}
		if _, err := m.client.CoreV1().Secrets(m.namespace).Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
			return "", fmt.Errorf("create secret %s: %w", name, err)
		}
		log.Printf("k8s-secrets: created secret %s during rotate", name)
		return password, nil
	}

	secret = secret.DeepCopy()
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data["password"] = []byte(password)
	delete(secret.StringData, "password")
	if _, err := m.client.CoreV1().Secrets(m.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update secret %s: %w", name, err)
	}
	log.Printf("k8s-secrets: rotated secret %s", name)
	return password, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestApplyK8sPassword|TestRotateSecret' -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Commit**

```bash
git add k8ssecrets.go k8ssecrets_test.go
git commit -m "feat: add applyK8sPassword wrapper and secret rotation"
```

---

### Task 4: Real in-cluster init, wired into `main()`

**Files:**
- Modify: `k8ssecrets.go`
- Modify: `main.go:70-77` (inside `main()`, alongside the existing `ADMIN_SITE` check)
- Test: `k8ssecrets_test.go`

**Interfaces:**
- Consumes: `k8sSecretsManager` (Task 2).
- Produces:
  - `func readNamespaceFile(path string) (string, error)` — used only internally by `initK8sSecretsManager`, but exposed for direct testing.
  - `func initK8sSecretsManager() *k8sSecretsManager` — called from `main()`.

- [ ] **Step 1: Write the failing tests for `readNamespaceFile`**

Add to `k8ssecrets_test.go` (needs `"os"`, `"path/filepath"` imports):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestReadNamespaceFile -v`
Expected: FAIL — `undefined: readNamespaceFile` (compile error).

- [ ] **Step 3: Implement `readNamespaceFile` and `initK8sSecretsManager`**

Append to `k8ssecrets.go` (add `"os"`, `"strings"`, `"k8s.io/client-go/rest"` imports):

```go
const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

func readNamespaceFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read namespace file %s: %w", path, err)
	}
	ns := strings.TrimSpace(string(data))
	if ns == "" {
		return "", fmt.Errorf("namespace file %s is empty", path)
	}
	return ns, nil
}

// initK8sSecretsManager builds a k8sSecretsManager from in-cluster config.
// USE_KUBERNETES_SECRETS only works inside a Kubernetes pod: it fails fast
// (log.Fatal) rather than let the provisioner run with unmanaged passwords.
func initK8sSecretsManager() *k8sSecretsManager {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("USE_KUBERNETES_SECRETS=true requires running inside a Kubernetes pod: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create Kubernetes client: %v", err)
	}
	namespace, err := readNamespaceFile(serviceAccountNamespaceFile)
	if err != nil {
		log.Fatalf("failed to determine Kubernetes namespace: %v", err)
	}
	log.Printf("k8s-secrets: enabled, namespace=%s", namespace)
	return &k8sSecretsManager{client: clientset, namespace: namespace}
}
```

- [ ] **Step 4: Wire into `main()`**

In `main.go`, the existing block reads:

```go
	if os.Getenv("ADMIN_SITE") == "true" {
		adminUser := os.Getenv("ADMIN_USER")
		adminPass := os.Getenv("ADMIN_PASSWORD")
		if adminUser == "" || adminPass == "" {
			log.Fatal("ADMIN_SITE=true requires ADMIN_USER and ADMIN_PASSWORD to be set")
		}
		go startAdminServer(getConfigPath())
	}

	go startBackupScheduler(getConfigPath())
```

Change it to:

```go
	if os.Getenv("ADMIN_SITE") == "true" {
		adminUser := os.Getenv("ADMIN_USER")
		adminPass := os.Getenv("ADMIN_PASSWORD")
		if adminUser == "" || adminPass == "" {
			log.Fatal("ADMIN_SITE=true requires ADMIN_USER and ADMIN_PASSWORD to be set")
		}
		go startAdminServer(getConfigPath())
	}

	if os.Getenv("USE_KUBERNETES_SECRETS") == "true" {
		secretsManager = initK8sSecretsManager()
	}

	go startBackupScheduler(getConfigPath())
```

- [ ] **Step 5: Run tests to verify they pass and everything still builds**

Run: `go test ./... -run TestReadNamespaceFile -v && go build ./...`
Expected: tests PASS; build succeeds with no errors.

- [ ] **Step 6: Commit**

```bash
git add k8ssecrets.go k8ssecrets_test.go main.go
git commit -m "feat: initialize k8sSecretsManager from in-cluster config in main()"
```

---

### Task 5: `processConfig` integration (SQL + MongoDB loops)

**Files:**
- Modify: `main.go:263-330` (the MongoDB loop and the SQL loop inside `processConfig`)
- Test: `main_test.go` (new file)

**Interfaces:**
- Consumes: `applyK8sPassword(ctx context.Context, serverName string, db DatabaseConfig) (DatabaseConfig, error)` (Task 3).

- [ ] **Step 1: Write the failing test**

`processConfig` itself dials real databases, so this task tests only the integration seam: that when `secretsManager` is set, the per-database loop calls `applyK8sPassword` and uses its result before provisioning. Since the SQL/Mongo provisioning calls are unexported functions that hit real drivers, add a narrow test that exercises the exact override behavior through `applyK8sPassword` directly with a `DatabaseConfig` shaped like what `processConfig` iterates over — this was already covered by `TestApplyK8sPassword_OverridesFromSecret` in Task 3. For this task, add a regression test that documents the intended call site by asserting the loop variable naming/order won't silently drop the override — write it as a table-driven test over `applyK8sPassword` with multiple databases in sequence, matching how `processConfig` iterates `server.Databases`.

Create `main_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestApplyK8sPassword_MultipleDatabasesGetDistinctSecrets -v`
Expected: This should actually PASS already since `applyK8sPassword` was implemented in Task 3 — confirming the unit is correct in isolation. Run it now to get a baseline PASS before touching `processConfig`.

- [ ] **Step 3: Integrate into the MongoDB loop in `processConfig`**

In `main.go`, find:

```go
		if dbType == MongoDB {
			// Process each database configuration for this server
			for i, dbConfig := range server.Databases {
				log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

				created, provErr := provisionMongoDB(server.RootConnectionString, dbConfig, server.DryRun)
```

Change to:

```go
		if dbType == MongoDB {
			// Process each database configuration for this server
			for i, dbConfig := range server.Databases {
				log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

				var k8sErr error
				dbConfig, k8sErr = applyK8sPassword(context.Background(), serverName, dbConfig)
				if k8sErr != nil {
					log.Printf("Failed to reconcile Kubernetes secret for %s on %s: %v", dbConfig.Database, serverName, k8sErr)
					continue
				}

				created, provErr := provisionMongoDB(server.RootConnectionString, dbConfig, server.DryRun)
```

- [ ] **Step 4: Integrate into the SQL loop in `processConfig`**

In `main.go`, find:

```go
		// Process each database configuration for this server
		for i, dbConfig := range server.Databases {
			log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

			var created bool
			var provErr error
			if dbType == MariaDB {
```

Change to:

```go
		// Process each database configuration for this server
		for i, dbConfig := range server.Databases {
			log.Printf("Processing database %d/%d on %s: %s", i+1, len(server.Databases), serverName, dbConfig.Database)

			var k8sErr error
			dbConfig, k8sErr = applyK8sPassword(context.Background(), serverName, dbConfig)
			if k8sErr != nil {
				log.Printf("Failed to reconcile Kubernetes secret for %s on %s: %v", dbConfig.Database, serverName, k8sErr)
				continue
			}

			var created bool
			var provErr error
			if dbType == MariaDB {
```

- [ ] **Step 5: Run the full test suite and build**

Run: `go build ./... && go test ./... -v`
Expected: build succeeds; all tests PASS (including pre-existing `admin_test.go` tests, unaffected since `secretsManager` is nil in those tests).

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: reconcile Kubernetes secret passwords in processConfig"
```

---

### Task 6: Admin UI — secret column, Rotate button, usage snippet

**Files:**
- Modify: `admin.go` (template, `adminTemplateData`, `newAdminHandler`, `handleIndex`, new `handleRotateSecret`)
- Test: `admin_test.go`

**Interfaces:**
- Consumes: `secretsManager` (Task 2), `secretNameFor` (Task 1), `(*k8sSecretsManager).rotateSecret` (Task 3).
- Produces: `POST /rotate-secret` route — no other task depends on this.

- [ ] **Step 1: Write the failing tests**

Add to `admin_test.go` (needs `"context"`, `corev1 "k8s.io/api/core/v1"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`, `"k8s.io/client-go/kubernetes/fake"` imports):

```go
func TestIndex_ShowsK8sSecretColumnWhenEnabled(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	wantName := secretNameFor("Test Server", "mydb")
	for _, want := range []string{wantName, "Rotate", "Kubernetes Secret"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
	if strings.Contains(body, `action="/update-password"`) {
		t.Error("did not expect manual password form when k8s secrets enabled")
	}
}

func TestIndex_HidesK8sSecretColumnWhenDisabled(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))
	secretsManager = nil

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Kubernetes Secret") {
		t.Error("did not expect Kubernetes Secret column when disabled")
	}
	if !strings.Contains(body, `action="/update-password"`) {
		t.Error("expected manual password form when k8s secrets disabled")
	}
}

func TestRotateSecret_Success(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	path := makeTestConfig(t, testConfigJSON)
	h := newAdminHandler(path)

	client := fake.NewSimpleClientset()
	secretsManager = &k8sSecretsManager{client: client, namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", w.Code, w.Body.String())
	}

	secret, err := client.CoreV1().Secrets("default").Get(context.Background(), secretNameFor("Test Server", "mydb"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if len(secret.Data["password"]) == 0 {
		t.Error("expected password to be set on the secret")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers[0].Databases[0].Password != "mypass" {
		t.Errorf("expected config.json password untouched, got %q", cfg.Servers[0].Databases[0].Password)
	}
}

func TestRotateSecret_DisabledWhenManagerNil(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))
	secretsManager = nil

	form := url.Values{"server_index": {"0"}, "db_index": {"0"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestRotateSecret_InvalidDBIndex(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "secret")
	h := newAdminHandler(makeTestConfig(t, testConfigJSON))

	secretsManager = &k8sSecretsManager{client: fake.NewSimpleClientset(), namespace: "default"}
	defer func() { secretsManager = nil }()

	form := url.Values{"server_index": {"0"}, "db_index": {"99"}}
	req := httptest.NewRequest("POST", "/rotate-secret", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
```

Add `"context"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`, and `"k8s.io/client-go/kubernetes/fake"` to `admin_test.go`'s import block (alongside its existing imports) — these three are the only new imports needed for the tests above.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestIndex_ShowsK8sSecretColumn|TestIndex_HidesK8sSecretColumn|TestRotateSecret' -v`
Expected: FAIL — `undefined: handleRotateSecret` and/or template doesn't contain "Kubernetes Secret"/"Rotate" text yet (route 404s, template assertions fail).

- [ ] **Step 3: Update `adminTemplateData` and the template**

In `admin.go`, change:

```go
type adminTemplateData struct {
	Servers    []DatabaseServer
	Flash      string
	FlashError bool
}
```

to:

```go
type adminTemplateData struct {
	Servers    []DatabaseServer
	Flash      string
	FlashError bool
	K8sEnabled bool
	Namespace  string
}
```

Change the `FuncMap` from:

```go
var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(`<!DOCTYPE html>
```

to:

```go
var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":       strings.Join,
	"secretName": secretNameFor,
}).Parse(`<!DOCTYPE html>
```

Replace the table header/row block:

```html
      <tr><th>Database</th><th>User</th><th>Permissions</th><th>Change Password</th></tr>
      {{range $di, $db := $server.Databases}}
      <tr>
        <td>{{$db.Database}}</td>
        <td>{{$db.User}}</td>
        <td>{{if $db.Permissions}}{{join $db.Permissions ", "}}{{else}}ALL{{end}}</td>
        <td>
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
        </td>
      </tr>
      {{end}}
```

with:

```html
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
```

Add a help block right before `<h2>Add Database</h2>`:

```html
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
```

- [ ] **Step 4: Populate the new template fields in `handleIndex`**

In `admin.go`, change:

```go
		msg := r.URL.Query().Get("msg")
		if err := adminTemplate.Execute(w, adminTemplateData{
			Servers:    cfg.Servers,
			Flash:      msg,
			FlashError: strings.HasPrefix(msg, "Error:"),
		}); err != nil {
```

to:

```go
		msg := r.URL.Query().Get("msg")
		data := adminTemplateData{
			Servers:    cfg.Servers,
			Flash:      msg,
			FlashError: strings.HasPrefix(msg, "Error:"),
			K8sEnabled: secretsManager != nil,
		}
		if secretsManager != nil {
			data.Namespace = secretsManager.namespace
		}
		if err := adminTemplate.Execute(w, data); err != nil {
```

- [ ] **Step 5: Add `handleRotateSecret` and register the route**

In `admin.go`, add the new imports `"context"` and `"time"` to the top-level import block, then add:

```go
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

		msg := fmt.Sprintf("Rotated Kubernetes secret %s", secretNameFor(serverName, dbName))
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}
```

Register it in `newAdminHandler`:

```go
func newAdminHandler(configPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex(configPath))
	mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
	mux.HandleFunc("/generate-password", handleGeneratePassword(configPath))
	mux.HandleFunc("/rotate-secret", handleRotateSecret(configPath))
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	return basicAuth(mux)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: all tests PASS, including every pre-existing `admin_test.go` test (they run with `secretsManager == nil`, so they exercise the unchanged `else` branch of the template).

- [ ] **Step 7: Commit**

```bash
git add admin.go admin_test.go
git commit -m "feat: show Kubernetes secret name and rotate control in admin UI"
```

---

### Task 7: RBAC manifests

**Files:**
- Modify: `kubernetes-deployment.yaml`
- Modify: `kubernetes-job.yaml`
- Modify: `kubernetes-with-secrets.yaml`

**Interfaces:**
- None (YAML only, no Go interfaces).

- [ ] **Step 1: Add ServiceAccount + RBAC to `kubernetes-deployment.yaml`**

Insert before the `Deployment` document (after the leading comment block, before the first `---` that precedes `kind: Deployment`):

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: homelab-db-provisioner
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
subjects:
- kind: ServiceAccount
  name: homelab-db-provisioner
  namespace: default
roleRef:
  kind: Role
  name: homelab-db-provisioner-secrets
  apiGroup: rbac.authorization.k8s.io
```

Then in the `Deployment`'s pod spec, add `serviceAccountName` and the env var (commented, since this is opt-in), so:

```yaml
    spec:
      serviceAccountName: homelab-db-provisioner
      containers:
      - name: provisioner
        image: homelab-db-provisioner:latest
        imagePullPolicy: IfNotPresent
        env:
        - name: CONFIG_PATH
          value: /config/config.json
        - name: WATCH_MODE
          value: "true"  # Enable watch mode to detect ConfigMap changes
        # - name: USE_KUBERNETES_SECRETS
        #   value: "true"  # Uncomment to generate per-database passwords as Secrets instead of using config.json passwords
```

- [ ] **Step 2: Add ServiceAccount + RBAC to `kubernetes-job.yaml`**

Insert before the `Job` document (after the leading comment block, before the `ConfigMap` document):

```yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: homelab-db-provisioner
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
subjects:
- kind: ServiceAccount
  name: homelab-db-provisioner
  namespace: default
roleRef:
  kind: Role
  name: homelab-db-provisioner-secrets
  apiGroup: rbac.authorization.k8s.io
```

Then in the `Job`'s pod spec, add `serviceAccountName` and the commented env var:

```yaml
    spec:
      serviceAccountName: homelab-db-provisioner
      containers:
      - name: provisioner
        image: homelab-db-provisioner:latest
        imagePullPolicy: IfNotPresent
        env:
        - name: CONFIG_PATH
          value: /config/config.json
        - name: WATCH_MODE
          value: "false"  # Run once and exit
        # - name: USE_KUBERNETES_SECRETS
        #   value: "true"  # Uncomment to generate per-database passwords as Secrets instead of using config.json passwords
```

- [ ] **Step 3: Add RBAC to `kubernetes-with-secrets.yaml`**

This file already defines a `ServiceAccount` named `homelab-db-provisioner` — do not duplicate it. Insert only the `Role` + `RoleBinding` block (same as Step 1) after the existing `ServiceAccount` document, and add the commented `USE_KUBERNETES_SECRETS` env var to the `Deployment`'s container env list, after the existing `WATCH_MODE` entry:

```yaml
        env:
        - name: CONFIG_PATH
          value: /config/config.json
        - name: WATCH_MODE
          value: "true"
        # - name: USE_KUBERNETES_SECRETS
        #   value: "true"  # Uncomment to generate per-database passwords as Secrets instead of using the secrets-provided passwords
```

- [ ] **Step 4: Validate YAML syntax for all three files**

Run:
```bash
python3 -c "
import yaml, sys
for path in ['kubernetes-deployment.yaml', 'kubernetes-job.yaml', 'kubernetes-with-secrets.yaml']:
    docs = list(yaml.safe_load_all(open(path)))
    kinds = [d.get('kind') for d in docs if d]
    print(path, '->', kinds)
"
```
Expected: no exceptions; each file's printed `kinds` list includes `ServiceAccount`, `Role`, and `RoleBinding` alongside its existing kinds (`Deployment`/`Job`/`Secret`/`ConfigMap`).

- [ ] **Step 5: Commit**

```bash
git add kubernetes-deployment.yaml kubernetes-job.yaml kubernetes-with-secrets.yaml
git commit -m "feat: add RBAC for Kubernetes Secrets mode to example manifests"
```

---

### Task 8: README documentation

**Files:**
- Modify: `README.md`

**Interfaces:**
- None.

- [ ] **Step 1: Add the env var to the existing table**

In `README.md`, find the "Environment Variables" table:

```markdown
| Variable | Default | Description |
|----------|---------|--------------|
| `CONFIG_PATH` | `/config/config.json` | Path to configuration file |
| `WATCH_MODE` | `false` | `true` to monitor config file and reprocess on changes |
| `ADMIN_SITE` | — | Set to `true` to enable the admin web UI |
| `ADMIN_USER` | — | Basic Auth username for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PASSWORD` | — | Basic Auth password for admin UI (required when `ADMIN_SITE=true`) |
| `ADMIN_PORT` | `8080` | Port for the admin web UI |
```

Add a row after `ADMIN_PORT`:

```markdown
| `USE_KUBERNETES_SECRETS` | `false` | `true` to generate per-database passwords into Kubernetes Secrets instead of using `config.json` passwords. Requires running inside a Kubernetes pod. See [Kubernetes Secrets Mode](#kubernetes-secrets-mode). |
```

- [ ] **Step 2: Add a new "Kubernetes Secrets Mode" section**

Insert a new section right after the existing "## Admin Web UI" section (before "## Backups"):

```markdown
## Kubernetes Secrets Mode

When `USE_KUBERNETES_SECRETS=true`, the provisioner ignores the `password`
field in `config.json` for every database entry. Instead, for each database
it gets-or-creates a Kubernetes `Secret` containing a randomly generated
password, and uses that password to create/update the database user. The
`password` field in `config.json` is never read or overwritten in this mode
— leave it blank or filled with a placeholder, it's ignored either way.

**This mode only works when the provisioner is running inside a Kubernetes
pod.** It uses in-cluster configuration (the pod's ServiceAccount token and
CA certificate) to talk to the Kubernetes API — there is no support for
running this mode via systemd or plain Docker outside a cluster.

### Secret naming

One Secret is created per database entry, named:

```
<slugified-server-name>-<slugified-database-name>-credentials
```

For example, a server named `"Main PostgreSQL"` with a database `"app_db"`
gets a Secret named `main-postgresql-app-db-credentials`. Each Secret has a
single key: `password`.

### Required RBAC

The pod's ServiceAccount needs permission to read and write Secrets in its
namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: homelab-db-provisioner-secrets
  namespace: default
subjects:
- kind: ServiceAccount
  name: homelab-db-provisioner
  namespace: default
roleRef:
  kind: Role
  name: homelab-db-provisioner-secrets
  apiGroup: rbac.authorization.k8s.io
```

`kubernetes-deployment.yaml`, `kubernetes-job.yaml`, and
`kubernetes-with-secrets.yaml` already include this Role/RoleBinding and a
`serviceAccountName` — just uncomment the `USE_KUBERNETES_SECRETS` env var
in whichever manifest you use.

**Security note:** RBAC can't restrict access by Secret name pattern, so
this grants the provisioner read/write access to *all* Secrets in its
namespace — the same trust level it already has via its root database
connection-string Secret. Run it in a dedicated namespace if you want
tighter isolation.

### Using the generated secret in your own app

Reference the generated Secret from your application's Deployment the same
way you'd reference any Kubernetes Secret:

```yaml
env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: main-postgresql-app-db-credentials
      key: password
```

Or inspect it directly:

```bash
kubectl get secret main-postgresql-app-db-credentials -n default \
  -o jsonpath='{.data.password}' | base64 -d
```

The admin UI (see [Admin Web UI](#admin-web-ui)) shows the exact secret name
for each database when this mode is enabled.

### Rotating a password

With the admin UI enabled, each database row shows its Secret name and a
**Rotate** button. Clicking it generates a new password and updates the
Secret in place — the provisioner picks up the new password on its next
reconcile pass (immediately in watch mode). There is no automatic rotation
schedule; rotation is manual.
```

- [ ] **Step 3: Verify the README renders sensible markdown**

Run:
```bash
python3 -c "
import re
text = open('README.md').read()
assert 'USE_KUBERNETES_SECRETS' in text
assert '## Kubernetes Secrets Mode' in text
assert text.count('```') % 2 == 0, 'unbalanced code fences'
print('README checks passed')
"
```
Expected: prints `README checks passed`, no assertion errors.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document Kubernetes Secrets mode"
```

---

### Task 9: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -v`
Expected: all tests PASS, zero failures, zero skips beyond what already existed.

- [ ] **Step 2: Run `go vet` and a full build**

Run: `go vet ./... && go build -o /tmp/provisioner-verify .`
Expected: no vet warnings; binary builds successfully.

- [ ] **Step 3: Run `go mod tidy` and confirm no diff**

Run: `go mod tidy && git diff --stat go.mod go.sum`
Expected: empty diff (dependencies already correctly pruned from Tasks 1–4); if there is a diff, stage and commit it.

- [ ] **Step 4: Commit any residual `go.mod`/`go.sum` cleanup (only if Step 3 produced a diff)**

```bash
git add go.mod go.sum
git commit -m "chore: go mod tidy"
```
