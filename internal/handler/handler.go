package handler

import (
	"net/http"

	"github-release-notifier/internal/metrics"
	"github-release-notifier/internal/subscription"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// Handler holds HTTP handler methods
type Handler struct {
	svc *subscription.Service
}

// New creates a Handler backed by the subscription service.
func New(svc *subscription.Service) *Handler {
	return &Handler{svc: svc}
}

// SubscribeRequest is the JSON body for POST /api/subscribe.
type SubscribeRequest struct {
	Email string `json:"email" binding:"required,email"`
	Repo  string `json:"repo" binding:"required"`
}

// Subscribe handles POST /api/subscribe
func (h *Handler) Subscribe(c *gin.Context) {
	var req SubscribeRequest

	// bind and validate JSON body
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Pass the request context so downstream calls (GitHub API, etc.)
	// are canceled if the client disconnects
	err := h.svc.Subscribe(c.Request.Context(), req.Email, req.Repo)
	if err == nil {
		metrics.SubscriptionsCreated.Inc()
		c.JSON(http.StatusOK, gin.H{"message": "subscription created, check your email to confirm"})
		return
	}

	// Map domain errors to HTTP statuses via the HTTPError interface (OCP):
	// the error itself knows its status, so adding new domain errors doesn't
	// require touching the handler.
	status, msg := translateError(err)
	c.JSON(status, gin.H{"error": msg})
}

// ConfirmSubscription handles GET /api/confirm/:token
func (h *Handler) ConfirmSubscription(c *gin.Context) {
	h.handleTokenAction(c, h.svc.Confirm, "subscription confirmed", metrics.SubscriptionsConfirmed)
}

// Unsubscribe handles GET /api/unsubscribe/:token
func (h *Handler) Unsubscribe(c *gin.Context) {
	h.handleTokenAction(c, h.svc.Unsubscribe, "unsubscribed successfully", metrics.Unsubscribes)
}

// handleTokenAction is a shared helper for endpoints that:
//   - take a :token URL parameter
//   - call a service method that operates on that token
//   - return a success message and increment a metric on success
//   - map ErrTokenNotFound to 404, anything else to 500
func (h *Handler) handleTokenAction(
	c *gin.Context,
	action func(token string) error,
	successMessage string,
	successMetric prometheus.Counter,
) {
	token := c.Param("token")

	err := action(token)
	if err == nil {
		successMetric.Inc()
		c.JSON(http.StatusOK, gin.H{"message": successMessage})
		return
	}

	status, msg := translateError(err)
	c.JSON(status, gin.H{"error": msg})
}

// GetSubscriptions handles GET /api/subscriptions?email={email}
func (h *Handler) GetSubscriptions(c *gin.Context) {
	email := c.Query("email")
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email query parameter is required"})
		return
	}

	subs, err := h.svc.GetSubscriptions(email)
	if err != nil {
		status, msg := translateError(err)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	// return empty array instead of nil (null)
	if subs == nil {
		subs = []subscription.Subscription{}
	}

	c.JSON(http.StatusOK, subs)
}

// translateError converts a service-layer error into an HTTP response
func translateError(err error) (status int, msg string) {
	switch subscription.KindOf(err) {
	case subscription.KindInvalid:
		return http.StatusBadRequest, err.Error()
	case subscription.KindNotFound:
		return http.StatusNotFound, err.Error()
	case subscription.KindConflict:
		return http.StatusConflict, err.Error()
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
