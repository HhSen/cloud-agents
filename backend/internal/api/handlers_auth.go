package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/pkg/config"
	"github.com/l-lab/cloud-agents/pkg/logger"
)

// auth request/response types

type passwordLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Email    string `json:"email,omitempty"`
}

// PasswordLoginHandler returns a Gin handler for POST /api/auth/login.
//
// @Summary      Password login
// @Description  Authenticate with username + password and receive an access token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      passwordLoginRequest  true  "Login credentials"
// @Success      200   {object}  tokenResponse
// @Failure      400   {object}  errorResponse  "username and password required"
// @Failure      401   {object}  errorResponse  "invalid credentials"
// @Failure      500   {object}  errorResponse  "internal error"
// @Router       /api/auth/login [post]
func PasswordLoginHandler(repo db.UserRepository, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body passwordLoginRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "username and password required"})
			return
		}
		user, err := repo.FindByCredentials(c.Request.Context(), body.Username, body.Password)
		if err != nil {
			logger.Default().Error("password login db error", "err", err)
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "internal error"})
			return
		}
		if user == nil {
			c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid credentials"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to issue token"})
			return
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token})
	}
}

// RegisterHandler handles POST /api/auth/register (username + password + email).
//
// @Summary      Register a user
// @Description  Create a new local user account and receive an access token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      registerRequest  true  "Registration request"
// @Success      201   {object}  tokenResponse
// @Failure      400   {object}  errorResponse  "username and password required"
// @Failure      409   {object}  errorResponse  "username already taken"
// @Failure      500   {object}  errorResponse  "failed to issue token"
// @Router       /api/auth/register [post]
func RegisterHandler(repo db.UserRepository, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body registerRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "username and password required"})
			return
		}
		if body.Email == "" {
			body.Email = body.Username + "@local"
		}
		user, err := repo.CreateWithPassword(c.Request.Context(), body.Username, body.Email, body.Password)
		if err != nil {
			logger.Default().Error("register user", "err", err)
			c.JSON(http.StatusConflict, errorResponse{Error: "username already taken"})
			return
		}
		ttl := time.Duration(authCfg.TokenTTLSeconds) * time.Second
		token, err := auth.CreateToken(authCfg.SecretKey, ttl, user.ID, user.UserName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to issue token"})
			return
		}
		c.JSON(http.StatusCreated, tokenResponse{AccessToken: token})
	}
}
