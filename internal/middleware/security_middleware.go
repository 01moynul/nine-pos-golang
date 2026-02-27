package middleware

import (
	"net/http"
	"strings"
	"time" // Added for DRM clock checking

	"go-pos-agent/internal/auth"
	"go-pos-agent/internal/database" // Added to access the DB
	"go-pos-agent/internal/models"   // Added to read the License schema

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

// CheckLicense enforces the Subscription DRM at the API layer.
// It intercepts requests, checks the clock, and triggers Lockdown Mode if expired.
func CheckLicense() gin.HandlerFunc {
	return func(c *gin.Context) {
		var license models.SystemLicense

		// Fetch the active license from the local database
		result := database.DB.Where("is_active = ?", true).First(&license)

		// Condition A: No license exists in the system at all
		if result.Error != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "No valid system license found. System locked.",
				"code":  "DRM_NO_LICENSE",
			})
			c.Abort()
			return
		}

		// Condition B: The subscription has expired
		if time.Now().After(license.ExpirationDate) {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": "Subscription Expired. Lockdown Mode Initiated.",
				"code":  "DRM_EXPIRED",
			})
			c.Abort()
			return
		}

		// Condition C: License is valid, allow the request to proceed to the handler
		c.Next()
	}
}
