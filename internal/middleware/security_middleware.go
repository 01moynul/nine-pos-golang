package middleware

import (
	"net/http"
	"strings"

	"go-pos-agent/internal/auth"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware checks if the user has a valid JWT token
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Get the token from the "Authorization" header
		// Format: "Bearer <token>"
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			c.Abort()
			return
		}

		// 2. Remove the "Bearer " prefix
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header must start with Bearer"})
			c.Abort()
			return
		}

		// 3. Validate the token using our auth package
		claims, err := auth.ValidateToken(tokenString)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// 4. Store user info in the context for the next handler (or AI Agent) to use
		c.Set("userID", claims.UserID)
		c.Set("role", claims.Role)

		// 5. Let the request pass to the next handler
		c.Next()
	}
}

// RequireRole is a secondary guard that checks for specific permissions
func RequireRole(allowedRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role != allowedRole {
			c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to access this resource"})
			c.Abort()
			return
		}
		c.Next()
	}
}
