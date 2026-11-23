// Package api serves the REST API (huma, OpenAPI generated from code), the
// OIDC login routes and the single-page frontend.
package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/auth"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/config"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

// Deps are the collaborators the API needs.
type Deps struct {
	DB         *db.DB
	Logger     *slog.Logger
	StaticFS   fs.FS  // built frontend; nil disables static serving
	SessionKey []byte // required when OIDC is enabled
	Mode       config.Mode
	StartedAt  time.Time
	Ring       *crypto.Ring     // encrypts stored secrets
	Hub        *progress.Hub    // live session updates; may be nil in tests that skip SSE
	Clients    *clients.Factory // builds Gmail and IMAP clients, owns secret encryption
}

type API struct {
	Router    *gin.Engine
	cfg       *config.Config
	db        *db.DB
	logger    *slog.Logger
	mode      config.Mode
	startedAt time.Time

	hub           *progress.Hub
	clients       *clients.Factory
	stateSealer   *auth.Sealer // signs the Google OAuth state cookie
	secureCookies bool
}

// devUser stands in for a signed-in user when authentication is disabled.
var devUser = auth.User{Subject: "local-dev", Name: "Local developer", PreferredUsername: "dev", Email: "dev@localhost"}

func New(cfg *config.Config, d Deps) (*API, error) {
	if cfg.LogFormat == "json" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(recovery(d.Logger), SlogMiddleware(d.Logger), securityHeaders())

	a := &API{
		Router: router, cfg: cfg, db: d.DB, logger: d.Logger, mode: d.Mode, startedAt: d.StartedAt,
		hub: d.Hub, clients: d.Clients, secureCookies: strings.HasPrefix(strings.ToLower(cfg.BaseURL), "https://"),
	}
	if a.clients == nil {
		a.clients = &clients.Factory{DB: d.DB, Ring: d.Ring, BaseURL: cfg.BaseURL}
	}
	key := d.SessionKey
	if len(key) == 0 { // authentication disabled: a per-process key is enough
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("random key: %w", err)
		}
	}
	a.stateSealer = auth.NewSealer(key, "google-oauth-state")

	registerOps(router, d.DB) // public

	baseHost := ""
	if u, err := url.Parse(cfg.BaseURL); err == nil {
		baseHost = u.Host
	}
	router.Use(auth.CSRFMiddleware(baseHost))

	if cfg.OIDCEnabled() {
		oidcAuth, err := auth.New(context.Background(), auth.Options{
			Issuer:        cfg.OIDCIssuer,
			ClientID:      cfg.OIDCClientID,
			ClientSecret:  cfg.OIDCClientSecret,
			BaseURL:       cfg.BaseURL,
			SessionKey:    d.SessionKey,
			Logger:        d.Logger,
			AllowedEmails: cfg.OIDCAllowedEmails,
			AllowedGroups: cfg.OIDCAllowedGroups,
		})
		if err != nil {
			return nil, fmt.Errorf("oidc setup: %w", err)
		}
		oidcAuth.Register(router) // login, callback and logout stay public
		router.Use(publicPaths(oidcAuth.Middleware()))
		if len(cfg.OIDCAllowedEmails)+len(cfg.OIDCAllowedGroups) == 0 {
			d.Logger.Warn("OIDC_ALLOWED_EMAILS and OIDC_ALLOWED_GROUPS are not set: every user of the identity provider can sign in and administer this application")
		}
		d.Logger.Info("OIDC authentication enabled", "issuer", cfg.OIDCIssuer, "public_client", cfg.OIDCClientSecret == "")
	} else {
		d.Logger.Warn("authentication disabled")
		router.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithUser(c.Request.Context(), devUser))
			c.Next()
		})
	}

	// Browser navigations and the event stream are plain gin routes (behind
	// the same authentication as the API).
	router.GET("/oauth/callback", a.oauthCallback)
	router.GET("/api/v1/events", a.eventsHandler)

	humaConfig := huma.DefaultConfig("Gmail Archive Sync API", "1.0.0")
	humaConfig.Info.Description = "Selectively replicate Gmail messages into a self-hosted IMAP server."
	api := humagin.New(router, humaConfig)
	a.registerRoutes(api)

	if d.StaticFS != nil {
		serveSPA(router, d.StaticFS)
	}
	return a, nil
}

// publicPaths lets the API documentation through without a session.
func publicPaths(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/docs" || strings.HasPrefix(p, "/docs/") || p == "/openapi.json" || p == "/openapi.yaml" {
			c.Next()
			return
		}
		next(c)
	}
}

// serveSPA serves built frontend files, falling back to index.html so client
// side routes work. Hashed assets are cached for a year; index.html never.
func serveSPA(router *gin.Engine, staticFS fs.FS) {
	fileServer := http.FileServer(http.FS(staticFS))
	router.NoRoute(func(c *gin.Context) {
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"title": "Not Found", "status": http.StatusNotFound})
			return
		}
		if f, err := staticFS.Open(p); p != "" && err == nil {
			_ = f.Close()
			if strings.HasPrefix(p, "assets/") {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

func (a *API) registerRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/api/v1/me",
		Summary:     "Get the signed-in user",
		Tags:        []string{"User"},
	}, a.getMe)

	huma.Register(api, huma.Operation{
		OperationID: "get-system-status",
		Method:      http.MethodGet,
		Path:        "/api/v1/system/status",
		Summary:     "Get application and dependency status",
		Tags:        []string{"System"},
	}, a.getSystemStatus)

	a.registerSettings(api)
	a.registerDestinations(api)
	a.registerAccounts(api)
	a.registerOAuth(api)
	a.registerSyncSettings(api)
	a.registerLabels(api)
	a.registerSync(api)
	a.registerSessions(api)
	a.registerFailures(api)
	a.registerOverview(api)
}
