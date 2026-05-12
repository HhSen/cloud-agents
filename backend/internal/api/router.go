package api

import (
	"fmt"
	"net/http"
	"time"

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
	Store       TaskStore
	Manager     SandboxManager
	FileStore   FileStore
	CORSOrigin  string
	DB          *gorm.DB      // nil → auth disabled (dev mode)
	Redis       *redis.Client // nil → CLI OIDC flow unavailable
	Cfg         *config.Config
	OIDCService *oidcpkg.Service // nil → OIDC routes not registered
	SSOService  *ssopkg.Service  // nil → SSO routes not registered
}

// NewRouter builds the HTTP handler for the tasks API.
func NewRouter(deps RouterDeps) http.Handler {
	h := NewHandler(deps.Store, deps.Manager, sandbox.NewProxy(), deps.FileStore)

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
	// Dev-only: no SSO/OIDC configured → allow username-only login to create a user record.
	if deps.SSOService == nil && deps.OIDCService == nil {
		r.GET("/api/auth/dev/login", devLoginHandler(deps.DB, deps.Cfg.Auth))
	}

	r.GET("/api/runtime-config", runtimeConfigHandler(deps.Cfg, deps.OIDCService, deps.SSOService))

	// ── Protected task routes ───────────────────────────────────────────────
	protected := r.Group("/api/tasks")
	protected.Use(auth.BearerAuth(deps.Cfg.Auth.SecretKey, deps.DB))
	{
		protected.POST("", h.CreateTask)
		protected.POST("/:id/messages", h.SendMessage)
		protected.GET("/:id", h.GetTask)
		protected.GET("/:id/history", h.GetTaskHistory)
		protected.DELETE("/:id", h.DeleteTask)
		protected.POST("/:id/permissions", h.RespondToPermission)
		protected.POST("/:id/questions", h.RespondToQuestion)
	}

	r.GET("/health", h.Health)
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return r
}

type runtimeConfigResponse struct {
	LoginMode     string `json:"loginMode"`
	DevLogin      bool   `json:"devLogin"`
	OIDCLoginText string `json:"oidcLoginText,omitempty"`
	SSOLoginText  string `json:"ssoLoginText,omitempty"`
}

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
			DevLogin:      hasPassword && !hasOIDC && !hasSSO,
			OIDCLoginText: cfg.OIDC.ClientID,
			SSOLoginText:  cfg.SSO.AppID,
		})
	}
}

func devLoginHandler(gormDB *gorm.DB, cfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		username := c.Query("username")
		if username == "" {
			c.String(http.StatusBadRequest, "missing username")
			return
		}
		user, err := db.FindOrCreate(gormDB, username, username+"@dev.local", db.AuthSourceDev)
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to find/create user")
			return
		}
		ttl := time.Duration(cfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(cfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to issue token")
			return
		}
		c.Redirect(http.StatusFound, fmt.Sprintf("%s/login/sso#access_token=%s", cfg.FrontendURL, token))
	}
}

func corsMiddleware(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
