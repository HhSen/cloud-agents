package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	_ "github.com/your-org/platform-backend/docs"
	"github.com/your-org/platform-backend/internal/sandbox"
)

// NewRouter builds the HTTP handler for the tasks API.
//
// Routes registered:
//
//	POST   /api/tasks                  – create a task
//	POST   /api/tasks/:id/messages     – send a message (streaming)
//	GET    /api/tasks/:id              – get task state
//	GET    /api/tasks/:id/history      – get conversation history (requires fileStore)
//	DELETE /api/tasks/:id              – delete a task
//	GET    /health                     – liveness probe
//
// All routes are wrapped with CORS middleware that allows requests from
// corsOrigin with methods GET, POST, DELETE, and OPTIONS.
func NewRouter(store TaskStore, mgr SandboxManager, corsOrigin string, fileStore FileStore) http.Handler {
	h := NewHandler(store, mgr, sandbox.NewProxy(), fileStore)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware(corsOrigin))

	r.POST("/api/tasks", h.CreateTask)
	r.POST("/api/tasks/:id/messages", h.SendMessage)
	r.GET("/api/tasks/:id", h.GetTask)
	r.GET("/api/tasks/:id/history", h.GetTaskHistory)
	r.DELETE("/api/tasks/:id", h.DeleteTask)
	r.GET("/health", h.Health)
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return r
}

func corsMiddleware(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
