package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/crypto"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/pkg/logger"
	"golang.org/x/crypto/ssh"
)

// UserHandler serves the user settings API.
type UserHandler struct {
	userRepo     db.UserRepository
	sshKeySecret string
}

// NewUserHandler constructs a UserHandler from its dependencies.
func NewUserHandler(repo db.UserRepository, sshKeySecret string) *UserHandler {
	return &UserHandler{
		userRepo:     repo,
		sshKeySecret: sshKeySecret,
	}
}

// user-domain request/response types

type userSettingsResponse struct {
	HasSSHKey bool `json:"has_ssh_key"`
}

type updateUserSettingsRequest struct {
	SSHPrivateKey *string `json:"ssh_private_key"`
}

// GetUserSettings handles GET /api/user/settings.
//
// @Summary      Get user settings
// @Description  Returns whether the authenticated user has an SSH key configured.
// @Tags         user
// @Produce      json
// @Success      200  {object}  userSettingsResponse
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Router       /api/user/settings [get]
func (h *UserHandler) GetUserSettings(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, userSettingsResponse{HasSSHKey: u.SSHPrivateKeyEnc != ""})
}

// UpdateUserSettings handles PUT /api/user/settings.
//
// @Summary      Update user settings
// @Description  Save or clear the user's SSH private key. Empty string clears the stored key.
// @Tags         user
// @Accept       json
// @Produce      json
// @Param        body  body      updateUserSettingsRequest  true  "Settings update"
// @Success      204
// @Failure      400  {object}  errorResponse  "invalid PEM or request body"
// @Failure      401  {object}  errorResponse  "unauthorized"
// @Failure      500  {object}  errorResponse  "internal error"
// @Router       /api/user/settings [put]
func (h *UserHandler) UpdateUserSettings(c *gin.Context) {
	u := auth.GetUser(c)
	if u == nil {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	if h.userRepo == nil {
		c.JSON(http.StatusServiceUnavailable, errorResponse{Error: "user settings not configured"})
		return
	}

	var body updateUserSettingsRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if body.SSHPrivateKey == nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "ssh_private_key is required (use empty string to clear)"})
		return
	}

	var encKey string
	if *body.SSHPrivateKey != "" {
		if _, err := ssh.ParseRawPrivateKey([]byte(*body.SSHPrivateKey)); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid SSH private key: " + err.Error()})
			return
		}
		if h.sshKeySecret == "" {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "SSH key encryption not configured"})
			return
		}
		enc, err := crypto.Encrypt(*body.SSHPrivateKey, h.sshKeySecret)
		if err != nil {
			logger.Default().Error("encrypt ssh key", "err", err)
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to encrypt key"})
			return
		}
		encKey = enc
	}

	if err := h.userRepo.UpdateSSHKey(c.Request.Context(), u.UserName, encKey); err != nil {
		logger.Default().Error("update ssh key", "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to save key"})
		return
	}

	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}
