package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTP request metrics
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// Business metrics
	SubscriptionsCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "subscriptions_created_total",
		Help: "Total number of subscriptions created",
	})

	SubscriptionsConfirmed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "subscriptions_confirmed_total",
		Help: "Total number of subscriptions confirmed",
	})

	Unsubscribes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "unsubscribes_total",
		Help: "Total number of unsubscribes",
	})

	// Scanner metrics
	ScannerRunsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "scanner_runs_total",
		Help: "Total number of scanner cycles executed",
	})

	ReleasesDetected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "releases_detected_total",
		Help: "Total number of new releases detected",
	})

	NotificationsSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "notifications_sent_total",
		Help: "Total number of release notification emails sent",
	})

	// GitHub API metrics
	GitHubAPICalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "github_api_calls_total",
			Help: "Total GitHub API calls",
		},
		[]string{"endpoint", "cache"},
	)
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		SubscriptionsCreated,
		SubscriptionsConfirmed,
		Unsubscribes,
		ScannerRunsTotal,
		ReleasesDetected,
		NotificationsSent,
		GitHubAPICalls,
	)
}

// GinMiddleware records HTTP request metrics for every request
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}

		HTTPRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		HTTPRequestDuration.WithLabelValues(c.Request.Method, path).Observe(duration)
	}
}

// Handler returns the Prometheus metrics HTTP handler
func Handler() gin.HandlerFunc {
	h := promhttp.Handler()
	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
