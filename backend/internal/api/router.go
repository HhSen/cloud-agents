package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	_ "github.com/your-org/platform-backend/docs"
	"github.com/your-org/platform-backend/internal/auth"
	"github.com/your-org/platform-backend/internal/db"
	oidcpkg "github.com/your-org/platform-backend/internal/oidc"
	"github.com/your-org/platform-backend/internal/sandbox"
	ssopkg "github.com/your-org/platform-backend/internal/sso"
	"github.com/your-org/platform-backend/pkg/config"
	"gorm.io/gorm"
)

// RouterDeps collects all wired-up dependencies for the router.
type RouterDeps struct {
	Store           TaskStore
	Manager         SandboxManager
	FileStore       FileStore
	KindsRepo       db.KindsRepository // nil → resource API unavailable
	OFSWriter       ResourceWriter     // nil → resource content upload unavailable
	WorkspaceReader WorkspaceReader    // nil → OFS workspace browsing unavailable
	CORSOrigin      string
	DB              *gorm.DB      // nil → auth disabled (dev mode)
	Redis           *redis.Client // nil → CLI OIDC flow unavailable
	Cfg             *config.Config
	OIDCService     *oidcpkg.Service // nil → OIDC routes not registered
	SSOService      *ssopkg.Service  // nil → SSO routes not registered
}

// NewRouter builds the HTTP handler for the tasks API.
func NewRouter(deps RouterDeps) http.Handler {
	h := NewHandler(deps.Store, deps.Manager, sandbox.NewProxy(), deps.FileStore)
	if deps.KindsRepo != nil {
		h.withResources(deps.KindsRepo, deps.OFSWriter)
	}
	if deps.Cfg != nil {
		h.withExecd(deps.Cfg.Sandbox.ServerURL, deps.Cfg.Sandbox.APIKey)
	}
	if deps.WorkspaceReader != nil {
		h.withWorkspace(deps.WorkspaceReader)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware(deps.CORSOrigin))

	// ── Public auth routes ──────────────────────────────────────────────────
	if deps.OIDCService != nil {
		oidcH := oidcpkg.NewHandlers(deps.OIDCService, deps.DB, deps.Cfg.Auth, deps.Cfg.OIDC, deps.Redis)
		r.GET("/api/auth/oidc/login", oidcH.Login)
		r.GET("/api/auth/oidc/callback", oidcH.Callback)
		if deps.Redis != nil {
			r.POST("/api/auth/oidc/cli-login", oidcH.CLILogin)
			r.GET("/api/auth/oidc/cli-callback", oidcH.CLICallback)
			r.GET("/api/auth/oidc/cli-poll", oidcH.CLIPoll)
		}
	}
	if deps.SSOService != nil {
		ssoH := ssopkg.NewHandlers(deps.SSOService, deps.DB, deps.Cfg.Auth)
		r.GET("/api/auth/sso/login", ssoH.Login)
		r.GET("/api/auth/sso/callback", ssoH.Callback)
	}
	r.POST("/api/auth/login", PasswordLoginHandler(deps.DB, deps.Cfg.Auth))
	r.POST("/api/auth/register", RegisterHandler(deps.DB, deps.Cfg.Auth))

	r.GET("/api/runtime-config", runtimeConfigHandler(deps.Cfg, deps.OIDCService, deps.SSOService))

	// ── Protected task routes ───────────────────────────────────────────────
	protected := r.Group("/api")
	protected.Use(auth.BearerAuth(deps.Cfg.Auth.SecretKey, deps.DB))
	{
		protected.GET("/tasks", h.ListTasks)
		protected.POST("/tasks", h.CreateTask)
		protected.POST("/tasks/:id/messages", h.SendMessage)
		protected.GET("/tasks/:id", h.GetTask)
		protected.GET("/tasks/:id/history", h.GetTaskHistory)
		protected.DELETE("/tasks/:id", h.DeleteTask)
		protected.POST("/tasks/:id/permissions", h.RespondToPermission)
		protected.POST("/tasks/:id/questions", h.RespondToQuestion)

		protected.POST("/resources", h.CreateResource)
		protected.POST("/resources/zip", h.CreateSkillFromZip)
		protected.GET("/resources", h.ListResources)
		protected.PUT("/resources/:id", h.UpdateResource)
		protected.DELETE("/resources/:id", h.DeleteResource)
		protected.PUT("/resources/:id/files/*filepath", h.UpsertSkillFile)
		protected.DELETE("/resources/:id/files/*filepath", h.DeleteSkillFile)

		protected.GET("/tasks/:id/workspace/files", h.WorkspaceFiles)
		protected.GET("/tasks/:id/workspace/file", h.WorkspaceFile)
		protected.Any("/tasks/:id/execd/*path", h.ExecdProxy)
	}

	r.GET("/health", h.Health)
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return r
}

// runtimeConfigHandler returns the auth/login modes the frontend should expose.
//
// @Summary      Frontend runtime config
// @Description  Returns which login modes (password, OIDC, SSO) are available to the frontend.
// @Tags         meta
// @Produce      json
// @Success      200  {object}  runtimeConfigResponse
// @Router       /api/runtime-config [get]
func runtimeConfigHandler(cfg *config.Config, oidcSvc *oidcpkg.Service, ssoSvc *ssopkg.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		hasOIDC := oidcSvc != nil
		hasSSO := ssoSvc != nil
		hasPassword := cfg.MySQL.DSN != ""

		mode := "none"
		switch {
		case hasOIDC && hasSSO && hasPassword:
			mode = "all"
		case hasOIDC && hasSSO:
			mode = "oidc+sso"
		case hasOIDC && hasPassword:
			mode = "oidc+password"
		case hasSSO && hasPassword:
			mode = "sso+password"
		case hasOIDC:
			mode = "oidc"
		case hasSSO:
			mode = "sso"
		case hasPassword:
			mode = "password"
		}

		c.JSON(http.StatusOK, runtimeConfigResponse{
			LoginMode:     mode,
			PasswordLogin: hasPassword && !hasOIDC && !hasSSO,
			AllowRegister: hasPassword && !hasOIDC && !hasSSO,
			OIDCLoginText: cfg.OIDC.ClientID,
			SSOLoginText:  cfg.SSO.AppID,
		})
	}
}

func corsMiddleware(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
