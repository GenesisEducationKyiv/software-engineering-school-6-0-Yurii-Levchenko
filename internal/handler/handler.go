package handler

import (
	"errors"
	"github-release-notifier/internal/metrics"
	"github-release-notifier/internal/model"
	"github-release-notifier/internal/service"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// Handler holds HTTP handler methods
type Handler struct {
	svc *service.Service
}

// create a new Handler
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// Subscribe handles POST /api/subscribe
func (h *Handler) Subscribe(c *gin.Context) {
	var req model.SubscribeRequest

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

	// map business errors to HTTP status codes (smh like exception handler)
	switch {
	case errors.Is(err, service.ErrInvalidEmail):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrInvalidRepoFormat):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrRepoNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrAlreadySubscribed):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
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

	if errors.Is(err, service.ErrTokenNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// GetSubscriptions handles GET /api/subscriptions?email={email}
func (h *Handler) GetSubscriptions(c *gin.Context) {
	email := c.Query("email") // Like request.GET.get('email') in Django
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email query parameter is required"})
		return
	}

	subs, err := h.svc.GetSubscriptions(email)
	if err != nil {
		if errors.Is(err, service.ErrInvalidEmail) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	// return empty array instead of nil (null)
	if subs == nil {
		subs = []model.Subscription{}
	}

	c.JSON(http.StatusOK, subs)
}
