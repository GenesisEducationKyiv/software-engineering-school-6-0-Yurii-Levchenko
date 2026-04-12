package handler

import (
	"errors"
	"github-release-notifier/internal/model"
	"github-release-notifier/internal/service"
	"net/http"

	"github.com/gin-gonic/gin"
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

	err := h.svc.Subscribe(req.Email, req.Repo)
	if err == nil {
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
	token := c.Param("token")

	err := h.svc.Confirm(token)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"message": "subscription confirmed"})
		return
	}

	if errors.Is(err, service.ErrTokenNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// Unsubscribe handles GET /api/unsubscribe/:token
func (h *Handler) Unsubscribe(c *gin.Context) {
	token := c.Param("token")

	err := h.svc.Unsubscribe(token)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"message": "unsubscribed successfully"})
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
