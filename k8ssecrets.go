package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func secretNameFor(serverName, database string) string {
	return fmt.Sprintf("%s-%s-credentials", slugify(serverName), slugify(database))
}

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
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"password": []byte(password)},
	}
	if _, err := m.client.CoreV1().Secrets(m.namespace).Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create secret %s: %w", name, err)
	}
	log.Printf("k8s-secrets: created secret %s", name)
	return password, nil
}

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
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"password": []byte(password)},
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
