package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDHeader is the HTTP header used to read/propagate a request id.
const RequestIDHeader = "X-Request-ID"

// RequestLogger is a Gin middleware that emits one structured (slog) access
// log line per request and attaches a request id for correlation.
//
// It replaces Gin's default text logger (the "[GIN] ... | 200 | ..." line).
// Each request gets an id (taken from the X-Request-ID header if the client
// sent one, otherwise generated), which is echoed back in the response header
// so a client/log reader can correlate a request across systems.
//
// The logged fields (method, path, status, duration_ms, request_id, client_ip)
// give the RED signals — rate, errors (via status), duration — at the log
// layer, complementing the Prometheus metrics.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		requestID := c.GetHeader(RequestIDHeader)
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Writer.Header().Set(RequestIDHeader, requestID)
		// Stash it so downstream handlers can read it if they ever log.
		c.Set("request_id", requestID)

		c.Next()

		duration := time.Since(start)
		status := c.Writer.Status()

		attrs := []any{
			"method", c.Request.Method,
			"path", c.FullPath(), // route template, not the raw URL (keeps cardinality low)
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"client_ip", c.ClientIP(),
			"request_id", requestID,
		}

		// Use a level that matches the outcome so error dashboards/queries
		// can filter by level alone.
		switch {
		case status >= 500:
			slog.Error("http request", attrs...)
		case status >= 400:
			slog.Warn("http request", attrs...)
		default:
			slog.Info("http request", attrs...)
		}
	}
}
