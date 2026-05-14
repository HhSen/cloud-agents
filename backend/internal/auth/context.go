package auth

import (
	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/db"
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

// GetUserID returns the authenticated user's database ID and true,
// or 0, false if no user is set on the context.
func GetUserID(c *gin.Context) (uint, bool) {
	u := GetUser(c)
	if u == nil {
		return 0, false
	}
	return u.ID, true
}
