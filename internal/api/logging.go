package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func SlogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		logger.Info("request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"duration", time.Since(start),
			"client_ip", c.ClientIP(),
		)
	}
}

// securityHeaders sets conservative defaults on every response. Rendered email
// gets its own, stricter Content-Security-Policy on its endpoint.
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		c.Next()
	}
}

// recovery turns a panic into a 500 and logs the panic value only. gin's
// default recovery dumps the request headers, which would write the session
// cookie to the log.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		logger.Error("panic in request handler", "method", c.Request.Method, "path", c.Request.URL.Path, "panic", fmt.Sprint(err))
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
