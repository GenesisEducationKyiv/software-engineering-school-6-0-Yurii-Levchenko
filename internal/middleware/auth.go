package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// APIKeyAuth returns a Gin middleware that checks for a valid API key
// in the X-API-Key header. If API_KEY is empty, authentication is disabled
// (allows the app to run without auth for development/testing)
func APIKeyAuth(apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// if no API key configured, skip authentication
		if apiKey == "" {
			c.Next()
			return
		}

		key := c.GetHeader("X-API-Key")
		if key == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing X-API-Key header",
			})
			return
		}

		if key != apiKey {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "invalid API key",
			})
			return
		}

		c.Next()
	}
}
