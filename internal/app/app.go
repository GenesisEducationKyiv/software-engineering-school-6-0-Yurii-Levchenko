// Package app wires HTTP routes for the application.
//
// BuildRouter is called from main.go (production) and from integration
// tests so both exercise the same router. External dependencies (DB,
// GitHub client, notifier) are constructed by the caller and reach this
// function via *service.Service.
package app

import (
	"github-release-notifier/internal/handler"
	"github-release-notifier/internal/metrics"
	"github-release-notifier/internal/middleware"
	"github-release-notifier/internal/service"

	"github.com/gin-gonic/gin"
)

// BuildRouter wires all HTTP routes and middleware.
//
// apiKey: if non-empty, X-API-Key header is required on /api/* routes.
// staticIndexPath: if non-empty, "/" serves that file as the subscription page;
// pass "" to skip the route (useful in integration tests where the file is not
// at a predictable relative path).
func BuildRouter(svc *service.Service, apiKey, staticIndexPath string) *gin.Engine {
	router := gin.Default()
	router.Use(metrics.GinMiddleware())

	if staticIndexPath != "" {
		router.StaticFile("/", staticIndexPath)
	}

	router.GET("/metrics", metrics.Handler())
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	h := handler.New(svc)
	api := router.Group("/api")
	api.Use(middleware.APIKeyAuth(apiKey))
	{
		api.POST("/subscribe", h.Subscribe)
		api.GET("/confirm/:token", h.ConfirmSubscription)
		api.GET("/unsubscribe/:token", h.Unsubscribe)
		api.GET("/subscriptions", h.GetSubscriptions)
	}

	return router
}
