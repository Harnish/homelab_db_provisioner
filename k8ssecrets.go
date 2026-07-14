package main

import (
	"context"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
