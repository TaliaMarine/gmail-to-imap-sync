package kube

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EnsureSecretValue returns the value stored under key in the named Secret.
// When the Secret does not exist it is created with value from generate, so a
// GitOps-managed chart needs no secret material. Several replicas racing to
// create it converge on the winner's value.
func EnsureSecretValue(ctx context.Context, client kubernetes.Interface, namespace, name, key string, generate func() (string, error), logger *slog.Logger) ([]byte, error) {
	secrets := client.CoreV1().Secrets(namespace)
	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		value, err := generate()
		if err != nil {
			return nil, err
		}
		s, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "gmail-archive-sync"},
			},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{key: value},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) { // another replica won the race
			s, err = secrets.Get(ctx, name, metav1.GetOptions{})
		} else if err == nil {
			logger.Info("created secret", "name", name)
			return []byte(value), nil
		}
		if err != nil {
			return nil, fmt.Errorf("create secret %s: %w", name, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("get secret %s: %w", name, err)
	}
	v := s.Data[key]
	if len(v) == 0 {
		return nil, fmt.Errorf("secret %s has no key %q", name, key)
	}
	return v, nil
}

// RandomBase64 returns n random bytes, base64url encoded.
func RandomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
