package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/kube"
	"k8s.io/client-go/kubernetes"
)

const sessionKeyBytes = 32

// LoadSessionKey returns the key that signs session cookies. In order of
// preference it uses the configured value, a Kubernetes Secret (created with
// a random key if missing) and finally a random in-memory key.
func LoadSessionKey(ctx context.Context, configured string, client kubernetes.Interface, namespace, secretName, secretKey string, logger *slog.Logger) ([]byte, error) {
	if configured != "" {
		if len(configured) < sessionKeyBytes {
			return nil, fmt.Errorf("SESSION_KEY must be at least %d characters", sessionKeyBytes)
		}
		return []byte(configured), nil
	}
	gen := func() (string, error) { return kube.RandomBase64(sessionKeyBytes) }
	if client != nil && secretName != "" {
		v, err := kube.EnsureSecretValue(ctx, client, namespace, secretName, secretKey, gen, logger)
		if err != nil {
			return nil, fmt.Errorf("session key: %w", err)
		}
		if len(v) < sessionKeyBytes {
			return nil, fmt.Errorf("secret %s key %q must hold at least %d bytes", secretName, secretKey, sessionKeyBytes)
		}
		return v, nil
	}
	key, err := gen()
	if err != nil {
		return nil, err
	}
	logger.Warn("no SESSION_KEY or session key secret configured, using a random key: sessions end on restart and are not shared between replicas")
	return []byte(key), nil
}
