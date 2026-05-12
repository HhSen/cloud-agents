package sso

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/your-org/platform-backend/internal/auth"
	"github.com/your-org/platform-backend/internal/db"
	"github.com/your-org/platform-backend/pkg/config"
	"gorm.io/gorm"
)

type Handlers struct {
	svc  *Service
	db   *gorm.DB
	cfg  config.AuthConfig
}

func NewHandlers(svc *Service, gormDB *gorm.DB, cfg config.AuthConfig) *Handlers {
	return &Handlers{svc: svc, db: gormDB, cfg: cfg}
}

// Login redirects the browser to the Didi SSO login page.
// An optional ?redirect= query param is forwarded to SSO as jumpto and echoed
// back in the callback, allowing the frontend to land on the right page.
func (h *Handlers) Login(c *gin.Context) {
	jumpto := c.Query("redirect")
	c.Redirect(http.StatusFound, h.svc.LoginURL(jumpto))
}

// Callback handles the SSO redirect: exchanges the code for a ticket, fetches user
// info, issues an app JWT, and redirects to the frontend.
func (h *Handlers) Callback(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.String(http.StatusBadRequest, "missing code")
		return
	}

	checkCode, err := h.svc.CheckCode(c.Request.Context(), code)
	if err != nil {
		c.String(http.StatusBadGateway, "SSO check_code failed")
		return
	}

	ticketResp, err := h.svc.CheckUserTicket(c.Request.Context(), checkCode.Ticket)
	if err != nil {
		c.String(http.StatusBadGateway, "SSO check_user_ticket failed")
		return
	}

	user, err := db.FindOrCreate(h.db, checkCode.UserName, ticketResp.Email, db.AuthSourceSSO)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to find/create user")
		return
	}

	ttl := time.Duration(h.cfg.TokenTTLSeconds) * time.Second
	token, err := auth.CreateToken(h.cfg.SecretKey, ttl, user.ID, user.UserName)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to issue token")
		return
	}

	target := fmt.Sprintf("%s/login/sso#access_token=%s", h.cfg.FrontendURL, token)
	c.Redirect(http.StatusFound, target)
}
