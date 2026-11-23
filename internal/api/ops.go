package api

import (
	"context"
	_ "embed"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
)

// privacyPage is the privacy policy, served without sign-in at /privacy so it
// can be used as the policy link of the Google consent screen when the
// application is reachable from the internet. docs/site/privacy.html is the
// same file for hosting elsewhere (a test keeps the two identical).
//
//go:embed privacy.html
var privacyPage []byte

// registerOps adds the unauthenticated health and metrics endpoints. They are
// served by every mode so probes and scraping work for worker-only pods.
func registerOps(router gin.IRoutes, database *db.DB) {
	router.GET("/health/live", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	router.GET("/health/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			c.String(http.StatusServiceUnavailable, "not ready")
			return
		}
		c.String(http.StatusOK, "ok")
	})
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/privacy", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "text/html; charset=utf-8", privacyPage)
	})
}

// NewOpsRouter serves only health and metrics, for --mode worker.
func NewOpsRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(recovery(d.Logger), SlogMiddleware(d.Logger), securityHeaders())
	registerOps(router, d.DB)
	return router
}
