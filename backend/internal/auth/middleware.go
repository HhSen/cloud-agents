package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/db"
	"gorm.io/gorm"
)

// BearerAuth extracts the Authorization header token and sets the User on the Gin context.
// Returns 401 if the token is missing or invalid.
func BearerAuth(secretKey string, gormDB *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" || !strings.HasPrefix(raw, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		token := strings.TrimPrefix(raw, "Bearer ")

		claims, err := VerifyToken(secretKey, token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		var u db.User
		if err := gormDB.First(&u, claims.UserID).Error; err != nil || !u.IsActive {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}

		SetUser(c, &u)
		c.Next()
	}
}
