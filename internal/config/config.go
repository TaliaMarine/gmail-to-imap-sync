// Package config loads infrastructure-level settings from the environment.
// Runtime settings (accounts, schedules, labels) live in PostgreSQL.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Mode selects which parts of the application a process runs.
type Mode string

const (
	ModeAll    Mode = "all"
	ModeWeb    Mode = "web"
	ModeWorker Mode = "worker"
)

// ParseMode validates a --mode value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeAll, ModeWeb, ModeWorker:
		return m, nil
	}
	return "", fmt.Errorf("invalid mode %q: want all, web or worker", s)
}

// Web reports whether the mode serves HTTP.
func (m Mode) Web() bool { return m == ModeAll || m == ModeWeb }

// Worker reports whether the mode runs the scheduler and job runner.
func (m Mode) Worker() bool { return m == ModeAll || m == ModeWorker }

type Config struct {
	HTTPPort    int
	DatabaseURL string
	BaseURL     string

	AuthDisabled bool

	// OIDC sign-in: Authorization Code + PKCE (S256); the client secret is
	// optional (public client).
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	// OIDCAllowedEmails/Groups restrict who may sign in (comma separated;
	// "@domain" allows a domain). Both empty: every user of the provider.
	OIDCAllowedEmails []string
	OIDCAllowedGroups []string

	// SessionKey signs session cookies; when empty it is read from (or
	// created in) the Kubernetes Secret SessionKeySecretName.
	SessionKey           string
	SessionKeySecretName string
	SessionKeySecretKey  string

	// EncryptionKeys is a key ring, "id:base64,id:base64" (first = active).
	// When empty it is read from (or created in) EncryptionKeySecretName.
	EncryptionKeys          string
	EncryptionKeySecretName string
	EncryptionKeySecretKey  string

	// MaxConcurrentSyncs caps sessions this process runs at once.
	MaxConcurrentSyncs int

	Namespace string
	LogLevel  string
	LogFormat string
}

func Load() *Config {
	return &Config{
		HTTPPort:    getEnvInt("HTTP_PORT", 1080),
		DatabaseURL: getEnv("DATABASE_URL", ""),
		BaseURL:     strings.TrimRight(getEnv("BASE_URL", ""), "/"),

		AuthDisabled: getEnvBool("AUTH_DISABLED", false),

		OIDCIssuer:        getEnv("OIDC_ISSUER", ""),
		OIDCClientID:      getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCAllowedEmails: splitList(getEnv("OIDC_ALLOWED_EMAILS", "")),
		OIDCAllowedGroups: splitList(getEnv("OIDC_ALLOWED_GROUPS", "")),

		SessionKey:           getEnv("SESSION_KEY", ""),
		SessionKeySecretName: getEnv("SESSION_KEY_SECRET_NAME", ""),
		SessionKeySecretKey:  getEnv("SESSION_KEY_SECRET_KEY", "key"),

		EncryptionKeys:          getEnv("ENCRYPTION_KEYS", ""),
		EncryptionKeySecretName: getEnv("ENCRYPTION_KEY_SECRET_NAME", ""),
		EncryptionKeySecretKey:  getEnv("ENCRYPTION_KEY_SECRET_KEY", "keys"),

		MaxConcurrentSyncs: getEnvInt("MAX_CONCURRENT_SYNCS", 2),

		Namespace: getEnv("POD_NAMESPACE", ""),
		LogLevel:  getEnv("LOG_LEVEL", "info"),
		LogFormat: getEnv("LOG_FORMAT", "json"),
	}
}

// OIDCEnabled reports whether browser sign-in is configured.
func (c *Config) OIDCEnabled() bool { return !c.AuthDisabled && c.OIDCIssuer != "" }

// Validate reports configuration that cannot work in the given mode (worker
// pods serve no UI, so they need no sign-in configuration).
func (c *Config) Validate(mode Mode) error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if !mode.Web() {
		return nil
	}
	if !c.AuthDisabled && c.OIDCIssuer == "" {
		return errors.New("configure OIDC_ISSUER/OIDC_CLIENT_ID, or set AUTH_DISABLED=true to run without authentication")
	}
	if c.OIDCEnabled() {
		if c.OIDCClientID == "" {
			return errors.New("OIDC_CLIENT_ID is required when OIDC_ISSUER is set")
		}
	}
	// BASE_URL forms the OIDC and Google OAuth redirect URIs.
	if c.BaseURL != "" || c.OIDCEnabled() {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("BASE_URL must be an absolute http(s) URL when OIDC is enabled")
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
