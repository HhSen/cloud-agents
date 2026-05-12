package auth

import (
	"github.com/gin-gonic/gin"
	"github.com/your-org/platform-backend/internal/db"
)

type contextKey string

const userContextKey contextKey = "auth_user"

func SetUser(c *gin.Context, u *db.User) {
	c.Set(string(userContextKey), u)
}

func GetUser(c *gin.Context) *db.User {
	v, exists := c.Get(string(userContextKey))
	if !exists {
		return nil
	}
	u, _ := v.(*db.User)
	return u
}
