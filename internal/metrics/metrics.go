package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// --- RED metrics for the HTTP API (Rate / Errors / Duration) ---
	//
	// Rate    : sum(rate(http_requests_total[5m]))            — requests per second
	// Errors  : http_requests_total{status=~"5.."}            — failing requests
	// Duration: histogram_quantile(0.95, http_request_duration_seconds) — p95 latency
	//
	// `path` is the route TEMPLATE (e.g. /api/confirm/:token), not the raw URL,
	// to keep label cardinality bounded (a raw URL with a token would explode it).
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests (RED: rate + errors via status label)",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds (RED: duration)",
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

	// --- RED-style metrics for the background scanner ---
	// Rate    : scanner_runs_total (cycles) / releases_detected / notifications_sent
	// Errors  : scanner_errors_total{stage}
	// Duration: scanner_cycle_duration_seconds
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

	// ScannerCycleDuration tracks how long one full scan cycle takes —
	// the "duration" signal for the background worker. Growing duration
	// is an early warning that the scanner can't keep up with its interval.
	ScannerCycleDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "scanner_cycle_duration_seconds",
		Help:    "Duration of one full scanner cycle in seconds (RED: duration)",
		Buckets: prometheus.DefBuckets,
	})

	// ScannerLastRunTimestamp holds the unix time of the last finished scan
	// cycle. Freshness signal: `time() - scanner_last_run_timestamp_seconds`
	// growing past the scan interval means the scanner is stuck or dead —
	// something rate() over counters cannot distinguish from "no work to do".
	ScannerLastRunTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "scanner_last_run_timestamp_seconds",
		Help: "Unix timestamp of the last finished scanner cycle",
	})

	// ScannerErrorsTotal counts failures by stage (github / tracking /
	// subscribers / notify) — the "errors" signal for the worker.
	ScannerErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scanner_errors_total",
			Help: "Total scanner errors by stage (RED: errors)",
		},
		[]string{"stage"},
	)

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
		ScannerCycleDuration,
		ScannerLastRunTimestamp,
		ScannerErrorsTotal,
		GitHubAPICalls,
	)
}

// GinMiddleware records HTTP request metrics for every request
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		// Don't record the metrics endpoint itself — every Prometheus scrape
		// hits /metrics, which would inflate request counts with self-noise.
		if path == "/metrics" {
			return
		}

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())

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
