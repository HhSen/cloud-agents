package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	_ "github.com/l-lab/cloud-agents/docs"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	oidcpkg "github.com/l-lab/cloud-agents/internal/oidc"
	"github.com/l-lab/cloud-agents/internal/sandbox"
	ssopkg "github.com/l-lab/cloud-agents/internal/sso"
	"github.com/l-lab/cloud-agents/pkg/config"
	"gorm.io/gorm"
)

// RouterDeps collects all wired-up dependencies for the router.
type RouterDeps struct {
	Store           TaskStore
	Manager         SandboxManager
	FileStore       FileStore
	KindsRepo       db.KindsRepository // nil → resource API returns 503
	OFSWriter       ResourceWriter     // nil → resource content upload returns 503
	OFSReader       ResourceReader     // nil → resource content read returns 503
	WorkspaceReader WorkspaceReader    // nil → OFS workspace browsing returns 409
	UserRepo        db.UserRepository  // nil → user settings update returns 503
	DB              *gorm.DB           // kept for auth.BearerAuth and OIDC/SSO middleware
	CORSOrigin      string
	Redis           *redis.Client    // nil → CLI OIDC flow unavailable
	Cfg             *config.Config
	OIDCService     *oidcpkg.Service // nil → OIDC routes not registered
	SSOService      *ssopkg.Service  // nil → SSO routes not registered
}

// NewRouter builds the HTTP handler for the tasks API.
func NewRouter(deps RouterDeps) http.Handler {
	taskH := NewTaskHandler(deps.Store, deps.Manager, sandbox.NewProxy(), deps.FileStore)

	resourceH := NewResourceHandler(deps.KindsRepo, deps.OFSWriter, deps.OFSReader)

	var serverURL, sandboxAPIKey string
	if deps.Cfg != nil {
		serverURL = deps.Cfg.Sandbox.ServerURL
		sandboxAPIKey = deps.Cfg.Sandbox.APIKey
	}
	workspaceH := NewWorkspaceHandler(deps.Store, deps.WorkspaceReader, serverURL, sandboxAPIKey)

	var sshKeySecret string
	if deps.Cfg != nil {
		sshKeySecret = deps.Cfg.Security.SSHKeySecret
	}
	userH := NewUserHandler(deps.UserRepo, sshKeySecret)

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
	r.POST("/api/auth/login", PasswordLoginHandler(deps.UserRepo, deps.Cfg.Auth))
	r.POST("/api/auth/register", RegisterHandler(deps.UserRepo, deps.Cfg.Auth))

	r.GET("/api/runtime-config", runtimeConfigHandler(deps.Cfg, deps.OIDCService, deps.SSOService))

	// ── Protected task routes ───────────────────────────────────────────────
	protected := r.Group("/api")
	protected.Use(auth.BearerAuth(deps.Cfg.Auth.SecretKey, deps.DB))
	{
		protected.GET("/tasks", taskH.ListTasks)
		protected.POST("/tasks", taskH.CreateTask)
		protected.POST("/tasks/:id/messages", taskH.SendMessage)
		protected.GET("/tasks/:id", taskH.GetTask)
		protected.GET("/tasks/:id/history", taskH.GetTaskHistory)
		protected.DELETE("/tasks/:id", taskH.DeleteTask)
		protected.POST("/tasks/:id/permissions", taskH.RespondToPermission)
		protected.POST("/tasks/:id/questions", taskH.RespondToQuestion)

		protected.POST("/resources", resourceH.CreateResource)
		protected.POST("/resources/zip", resourceH.CreateSkillFromZip)
		protected.GET("/resources", resourceH.ListResources)
		protected.GET("/resources/:id/content", resourceH.GetSkillContent)
		protected.PUT("/resources/:id", resourceH.UpdateResource)
		protected.DELETE("/resources/:id", resourceH.DeleteResource)
		protected.PUT("/resources/:id/files/*filepath", resourceH.UpsertSkillFile)
		protected.DELETE("/resources/:id/files/*filepath", resourceH.DeleteSkillFile)

		protected.GET("/tasks/:id/workspace/files", workspaceH.WorkspaceFiles)
		protected.GET("/tasks/:id/workspace/file", workspaceH.WorkspaceFile)
		protected.Any("/tasks/:id/execd/*path", workspaceH.ExecdProxy)

		protected.GET("/user/settings", userH.GetUserSettings)
		protected.PUT("/user/settings", userH.UpdateUserSettings)
	}

	r.GET("/health", taskH.Health)
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
