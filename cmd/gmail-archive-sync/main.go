package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/api"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/auth"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/config"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/jobs"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/kube"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/version"
)

func main() {
	modeFlag := flag.String("mode", string(config.ModeAll), "what to run: all, web or worker")
	rotate := flag.Bool("rotate-keys", false, "re-encrypt stored secrets with the active encryption key, then exit")
	flag.Parse()

	cfg := config.Load()
	logger := setupLogger(cfg)
	mode, err := config.ParseMode(*modeFlag)
	if err == nil {
		if *rotate {
			err = rotateKeys(cfg, logger)
		} else {
			err = run(cfg, mode, logger)
		}
	}
	if err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config, mode config.Mode, logger *slog.Logger) error {
	if err := cfg.Validate(mode); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startedAt := time.Now()
	logger.Info("starting", "version", version.Version, "mode", mode)

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("database ready")

	var kubeClient kubernetes.Interface
	namespace := cfg.Namespace
	if kube.InCluster() {
		if kubeClient, err = kube.NewClient(); err != nil {
			return fmt.Errorf("kubernetes client: %w", err)
		}
		if namespace, err = kube.Namespace(cfg.Namespace); err != nil {
			return err
		}
	}

	ring, err := loadEncryptionRing(ctx, cfg, kubeClient, namespace, logger)
	if err != nil {
		return fmt.Errorf("encryption keys: %w", err)
	}
	factory := &clients.Factory{DB: database, Ring: ring, BaseURL: cfg.BaseURL}
	hub := progress.NewHub(database.Pool, logger)
	go hub.Run(ctx)

	var sessionKey []byte
	if mode.Web() && cfg.OIDCEnabled() {
		sessionKey, err = auth.LoadSessionKey(ctx, cfg.SessionKey, kubeClient, namespace, cfg.SessionKeySecretName, cfg.SessionKeySecretKey, logger)
		if err != nil {
			return fmt.Errorf("session key: %w", err)
		}
	}

	errc := make(chan error, 2)
	deps := api.Deps{
		DB: database, Logger: logger, SessionKey: sessionKey, Mode: mode, StartedAt: startedAt,
		Ring: ring, Hub: hub, Clients: factory,
	}
	var handler http.Handler
	if mode.Web() {
		if st, err := os.Stat("web/dist"); err == nil && st.IsDir() {
			deps.StaticFS = os.DirFS("web/dist")
			logger.Info("serving frontend from web/dist")
		}
		a, err := api.New(cfg, deps)
		if err != nil {
			return fmt.Errorf("initialize API: %w", err)
		}
		handler = a.Router
	} else {
		handler = api.NewOpsRouter(deps) // worker pods still expose probes and metrics
	}
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	logger.Info("HTTP server starting", "addr", httpServer.Addr)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http: %w", err)
		}
	}()

	// The worker claims and runs sessions, enqueues scheduled ones and tends
	// to sessions of dead workers. On shutdown it winds sessions down (they
	// are saved and requeued) before the database is closed.
	var workerDone chan struct{}
	if mode.Worker() {
		runner := jobs.NewRunner(jobs.Options{
			DB: database, Builder: &jobs.DBBuilder{DB: database, Clients: factory}, Logger: logger, Hub: hub,
			MaxConcurrent: cfg.MaxConcurrentSyncs,
			Notifier:      &jobs.Notifier{DB: database, Clients: factory, Logger: logger},
		})
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); runner.Run(ctx) }()
			go func() { defer wg.Done(); runner.Maintain(ctx) }()
			wg.Wait()
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
	if workerDone != nil {
		<-workerDone
	}
	logger.Info("shutdown complete")
	return nil
}

// rotateKeys re-encrypts every stored secret with the active key (the first
// of ENCRYPTION_KEYS), so older keys can be dropped afterwards.
func rotateKeys(cfg *config.Config, logger *slog.Logger) error {
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if cfg.EncryptionKeys == "" {
		return errors.New("rotate-keys needs ENCRYPTION_KEYS: the new key first, then the old ones")
	}
	ring, err := crypto.ParseRing(cfg.EncryptionKeys)
	if err != nil {
		return fmt.Errorf("encryption keys: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	st, err := (&clients.Factory{DB: database, Ring: ring}).RotateKeys(ctx)
	if err != nil {
		return err
	}
	logger.Info("keys rotated", "re_encrypted", st.Rotated, "already_current", st.Current, "active_key", ring.ActiveKeyID())
	return nil
}

// loadEncryptionRing reads the key ring from ENCRYPTION_KEYS or a Kubernetes
// Secret that is created with a fresh key on first start. Outside a cluster
// without ENCRYPTION_KEYS a throwaway key is generated, with a warning.
func loadEncryptionRing(ctx context.Context, cfg *config.Config, client kubernetes.Interface, namespace string, logger *slog.Logger) (*crypto.Ring, error) {
	spec := cfg.EncryptionKeys
	switch {
	case spec != "":
	case client != nil && cfg.EncryptionKeySecretName != "":
		v, err := kube.EnsureSecretValue(ctx, client, namespace, cfg.EncryptionKeySecretName, cfg.EncryptionKeySecretKey,
			func() (string, error) { return crypto.GenerateRingSpec("k1") }, logger)
		if err != nil {
			return nil, err
		}
		spec = string(v)
	default:
		var err error
		if spec, err = crypto.GenerateRingSpec("ephemeral"); err != nil {
			return nil, err
		}
		logger.Warn("no ENCRYPTION_KEYS configured, using a throwaway key: stored credentials will be unreadable after a restart")
	}
	return crypto.ParseRing(spec)
}

func setupLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "plain" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
