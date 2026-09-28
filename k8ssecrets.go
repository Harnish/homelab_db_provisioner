package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// defaultSecretName is the Secret name offered when a database is first put
// in a Secret. The chosen name is then stored in DatabaseConfig.K8sSecret, so
// renaming the server or moving the entry to another server keeps it.
func defaultSecretName(database string) string {
	return slugify(database) + "-credentials"
}

// claimSecretName validates requested (blank means the default for database)
// and checks no other live config entry already uses it. Migrated-away
// tombstones don't count: their entry on the target server shares the Secret.
func claimSecretName(cfg *Config, requested, database string) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = defaultSecretName(database)
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return "", fmt.Errorf("invalid secret name %q: %s", name, strings.Join(errs, "; "))
	}
	for _, s := range cfg.Servers {
		for _, d := range s.Databases {
			if d.K8sSecret == name && (d.Migrate == nil || !d.Migrate.Completed) {
				return "", fmt.Errorf("secret name %q is already used by %s/%s", name, s.Name, d.Database)
			}
		}
	}
	return name, nil
}

// buildConnectString rebuilds rootConnStr with db's own credentials and
// database name, keeping the original scheme/host/port. Works for
// postgres://, mariadb://, mysql://, and mongodb:// alike since they're all
// plain URLs at this level (the driver-specific DSN conversion in
// processConfig happens separately, only for the actual driver connection).
func buildConnectString(rootConnStr string, db DatabaseConfig, password string) (string, error) {
	u, err := url.Parse(rootConnStr)
	if err != nil {
		return "", fmt.Errorf("parse root connection string: %w", err)
	}
	u.User = url.UserPassword(db.User, password)
	u.Path = "/" + db.Database
	return u.String(), nil
}

// k8sSecretsManager reconciles per-database passwords against Kubernetes Secrets.
// client is kubernetes.Interface (not *kubernetes.Clientset) so tests can inject a fake clientset.
type k8sSecretsManager struct {
	client    kubernetes.Interface
	namespace string
}

// secretsManager is nil unless USE_KUBERNETES_SECRETS=true.
var secretsManager *k8sSecretsManager

const (
	k8sManagedByLabel = "app.kubernetes.io/managed-by"
	k8sManagedByValue = "homelab-db-provisioner"
)

// managedSecret fetches a Secret, or returns nil if it doesn't exist. It
// refuses a Secret this provisioner didn't create, so a name like
// "app-credentials" can't take over another app's Secret in the namespace.
func (m *k8sSecretsManager) managedSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	secret, err := m.client.CoreV1().Secrets(m.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get secret %s: %w", name, err)
	}
	if secret.Labels[k8sManagedByLabel] != k8sManagedByValue {
		return nil, fmt.Errorf("secret %s exists but isn't managed by %s; choose another name", name, k8sManagedByValue)
	}
	return secret, nil
}

func (m *k8sSecretsManager) createSecret(ctx context.Context, name string, data map[string][]byte) error {
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: m.namespace,
			Labels:    map[string]string{k8sManagedByLabel: k8sManagedByValue},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if _, err := m.client.CoreV1().Secrets(m.namespace).Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create secret %s: %w", name, err)
	}
	return nil
}

// secretData is the Secret contents for db with the given password.
func secretData(rootConnStr string, db DatabaseConfig, password string) (map[string][]byte, error) {
	data := map[string][]byte{"password": []byte(password)}
	if db.RequiresConnectString {
		connStr, err := buildConnectString(rootConnStr, db, password)
		if err != nil {
			return nil, fmt.Errorf("build connection string for %s: %w", db.K8sSecret, err)
		}
		data["connection_string"] = []byte(connStr)
	}
	return data, nil
}

// reconcilePassword returns db's live password from its Secret (db.K8sSecret),
// creating the Secret if it's missing. A new Secret is seeded from the config
// password when there is one, so moving a database doesn't rotate it.
func (m *k8sSecretsManager) reconcilePassword(ctx context.Context, rootConnStr string, db DatabaseConfig) (string, error) {
	name := db.K8sSecret
	secret, err := m.managedSecret(ctx, name)
	if err != nil {
		return "", err
	}
	if secret != nil {
		pw, ok := secret.Data["password"]
		if !ok || len(pw) == 0 {
			return "", fmt.Errorf("secret %s exists but has no password key", name)
		}
		if db.RequiresConnectString && len(secret.Data["connection_string"]) == 0 {
			connStr, err := buildConnectString(rootConnStr, db, string(pw))
			if err != nil {
				return "", fmt.Errorf("build connection string for %s: %w", name, err)
			}
			secret = secret.DeepCopy()
			secret.Data["connection_string"] = []byte(connStr)
			if _, err := m.client.CoreV1().Secrets(m.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				return "", fmt.Errorf("update secret %s with connection_string: %w", name, err)
			}
			log.Printf("k8s-secrets: added connection_string to existing secret %s", name)
		}
		return string(pw), nil
	}

	password := db.Password
	if password == "" {
		if password, err = generatePassword(); err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
	}
	data, err := secretData(rootConnStr, db, password)
	if err != nil {
		return "", err
	}
	if err := m.createSecret(ctx, name, data); err != nil {
		return "", err
	}
	log.Printf("k8s-secrets: created secret %s", name)
	return password, nil
}

// applyK8sPassword fills db.Password from its Secret when db.K8sSecret is set.
// Databases without it keep their config password and never touch Kubernetes.
func applyK8sPassword(ctx context.Context, rootConnStr string, db DatabaseConfig) (DatabaseConfig, error) {
	if db.K8sSecret == "" {
		return db, nil
	}
	if secretsManager == nil {
		if db.Password == "" {
			return db, fmt.Errorf("k8s_secret is set but USE_KUBERNETES_SECRETS is off; refusing to provision %s with an empty password", db.Database)
		}
		return db, nil
	}
	password, err := secretsManager.reconcilePassword(ctx, rootConnStr, db)
	if err != nil {
		return db, err
	}
	db.Password = password
	return db, nil
}

func (m *k8sSecretsManager) rotateSecret(ctx context.Context, rootConnStr string, db DatabaseConfig) (string, error) {
	name := db.K8sSecret
	if name == "" {
		return "", fmt.Errorf("%s is not stored in a Kubernetes Secret", db.Database)
	}
	password, err := generatePassword()
	if err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	data, err := secretData(rootConnStr, db, password)
	if err != nil {
		return "", err
	}

	secret, err := m.managedSecret(ctx, name)
	if err != nil {
		return "", err
	}
	if secret == nil {
		if err := m.createSecret(ctx, name, data); err != nil {
			return "", err
		}
		log.Printf("k8s-secrets: created secret %s during rotate", name)
		return password, nil
	}

	secret = secret.DeepCopy()
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	for k, v := range data {
		secret.Data[k] = v
		delete(secret.StringData, k)
	}
	if _, err := m.client.CoreV1().Secrets(m.namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update secret %s: %w", name, err)
	}
	log.Printf("k8s-secrets: rotated secret %s", name)
	return password, nil
}

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
