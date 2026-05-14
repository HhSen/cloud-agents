package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/l-lab/cloud-agents/internal/auth"
	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/pkg/config"
	"gorm.io/gorm"
)

type statePayload struct {
	Nonce      string `json:"nonce"`
	Redirect   string `json:"redirect,omitempty"`
	CLISession string `json:"cli_session,omitempty"`
	jwt.RegisteredClaims
}

type cliSession struct {
	Status string `json:"status"` // "pending" | "completed"
	Token  string `json:"token,omitempty"`
}

type cliLoginRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

type cliLoginResponse struct {
	AuthURL string `json:"auth_url"`
}

type cliPollResponse struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type Handlers struct {
	svc   *Service
	db    *gorm.DB
	cfg   config.AuthConfig
	oidc  config.OIDCConfig
	redis *redis.Client
}

func NewHandlers(svc *Service, gormDB *gorm.DB, cfg config.AuthConfig, oidcCfg config.OIDCConfig, rdb *redis.Client) *Handlers {
	return &Handlers{svc: svc, db: gormDB, cfg: cfg, oidc: oidcCfg, redis: rdb}
}

func (h *Handlers) buildStateJWT(nonce, redirect, cliSessionID string) (string, error) {
	ttl := time.Duration(h.cfg.StateTTLSeconds) * time.Second
	claims := statePayload{
		Nonce:      nonce,
		Redirect:   redirect,
		CLISession: cliSessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(h.cfg.OIDCStateSecret))
}

func (h *Handlers) parseStateJWT(stateStr string) (*statePayload, error) {
	tok, err := jwt.ParseWithClaims(stateStr, &statePayload{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(h.cfg.OIDCStateSecret), nil
	})
	if err != nil || !tok.Valid {
		return nil, err
	}
	return tok.Claims.(*statePayload), nil
}

func (h *Handlers) issueAppToken(userID uint, userName string) (string, error) {
	ttl := time.Duration(h.cfg.TokenTTLSeconds) * time.Second
	return auth.CreateToken(h.cfg.SecretKey, ttl, userID, userName)
}

// Login redirects the browser to the OIDC provider's authorization endpoint.
//
// @Summary      OIDC browser login
// @Description  Redirects the browser to the OIDC provider's authorization endpoint. The optional ?redirect= is preserved through the flow.
// @Tags         auth
// @Param        redirect  query     string  false  "Frontend redirect target after callback"
// @Success      302
// @Failure      500       {string}  string  "failed to build state or auth URL"
// @Router       /api/auth/oidc/login [get]
func (h *Handlers) Login(c *gin.Context) {
	nonce := uuid.New().String()
	redirect := c.Query("redirect")
	stateJWT, err := h.buildStateJWT(nonce, redirect, "")
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to build state")
		return
	}
	authURL, err := h.svc.AuthURL(c.Request.Context(), h.oidc.RedirectURI, stateJWT, nonce)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to build auth URL")
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// Callback handles the OIDC redirect, verifies state + id_token, issues an app JWT,
// then redirects to the frontend /login/oidc page with ?access_token=.
//
// @Summary      OIDC browser callback
// @Description  Verifies state and id_token returned by the OIDC provider, issues an app JWT, and redirects to the frontend with the token in the URL fragment.
// @Tags         auth
// @Param        state  query     string  true  "State JWT issued during /api/auth/oidc/login"
// @Param        code   query     string  true  "Authorization code from the OIDC provider"
// @Success      302
// @Failure      400    {string}  string  "invalid state"
// @Failure      401    {string}  string  "id_token verification failed"
// @Failure      500    {string}  string  "internal error"
// @Failure      502    {string}  string  "code exchange failed or missing id_token"
// @Router       /api/auth/oidc/callback [get]
func (h *Handlers) Callback(c *gin.Context) {
	h.handleCallback(c, h.oidc.RedirectURI, func(token, _ string) {
		target := fmt.Sprintf("%s/login/oidc#access_token=%s", h.cfg.FrontendURL, token)
		c.Redirect(http.StatusFound, target)
	})
}

// CLILogin creates a pending Redis session and returns the browser auth URL.
//
// @Summary      Start CLI OIDC login
// @Description  Creates a pending Redis session keyed by session_id and returns the browser auth URL the CLI should open.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body      cliLoginRequest  true  "CLI session"
// @Success      200   {object}  cliLoginResponse
// @Failure      400   {object}  errorResponse  "session_id required"
// @Failure      500   {object}  errorResponse  "failed to build state or auth URL"
// @Router       /api/auth/oidc/cli-login [post]
func (h *Handlers) CLILogin(c *gin.Context) {
	var body cliLoginRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "session_id required"})
		return
	}

	nonce := uuid.New().String()
	stateJWT, err := h.buildStateJWT(nonce, "", body.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to build state"})
		return
	}

	sess := cliSession{Status: "pending"}
	data, _ := json.Marshal(sess)
	key := cliRedisKey(body.SessionID)
	if err := h.redis.Set(c.Request.Context(), key, data, 5*time.Minute).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "redis error"})
		return
	}

	authURL, err := h.svc.AuthURL(c.Request.Context(), h.oidc.CLIRedirectURI, stateJWT, nonce)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to build auth URL"})
		return
	}
	c.JSON(http.StatusOK, cliLoginResponse{AuthURL: authURL})
}

// CLICallback handles the OIDC redirect for the CLI flow; writes the app JWT to Redis.
//
// @Summary      OIDC CLI callback
// @Description  Verifies the OIDC response for a CLI flow and stores the issued app token in Redis under the session ID.
// @Tags         auth
// @Param        state  query     string  true  "State JWT issued during cli-login"
// @Param        code   query     string  true  "Authorization code from the OIDC provider"
// @Success      200    {string}  string  "Authentication successful"
// @Failure      400    {string}  string  "invalid state or missing cli session"
// @Failure      401    {string}  string  "id_token verification failed"
// @Failure      500    {string}  string  "internal error"
// @Failure      502    {string}  string  "code exchange failed or failed to store session"
// @Router       /api/auth/oidc/cli-callback [get]
func (h *Handlers) CLICallback(c *gin.Context) {
	h.handleCallback(c, h.oidc.CLIRedirectURI, func(token, sessionID string) {
		if sessionID == "" {
			c.String(http.StatusBadRequest, "missing cli session")
			return
		}
		sess := cliSession{Status: "completed", Token: token}
		data, _ := json.Marshal(sess)
		if err := h.redis.Set(c.Request.Context(), cliRedisKey(sessionID), data, 5*time.Minute).Err(); err != nil {
			c.String(http.StatusBadGateway, "failed to store session")
			return
		}
		c.String(http.StatusOK, "Authentication successful. You may close this tab.")
	})
}

// CLIPoll returns the current status of a CLI session.
//
// @Summary      Poll CLI OIDC session
// @Description  Returns the status of a CLI login session. When status is "completed" the response also contains the issued access token, and the session is deleted.
// @Tags         auth
// @Produce      json
// @Param        session_id  query     string  true  "CLI session ID"
// @Success      200         {object}  cliPollResponse
// @Failure      400         {object}  errorResponse  "session_id required"
// @Failure      404         {object}  errorResponse  "session not found"
// @Failure      500         {object}  errorResponse  "redis error or invalid session data"
// @Router       /api/auth/oidc/cli-poll [get]
func (h *Handlers) CLIPoll(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "session_id required"})
		return
	}
	key := cliRedisKey(sessionID)
	data, err := h.redis.Get(c.Request.Context(), key).Bytes()
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, cliPollResponse{Status: "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "redis error"})
		return
	}
	var sess cliSession
	if err := json.Unmarshal(data, &sess); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "invalid session data"})
		return
	}
	if sess.Status == "completed" {
		h.redis.Del(context.Background(), key)
		c.JSON(http.StatusOK, cliPollResponse{Status: "completed", Token: sess.Token})
		return
	}
	c.JSON(http.StatusOK, cliPollResponse{Status: sess.Status})
}

// handleCallback is the shared verify-and-issue logic for both web and CLI callbacks.
func (h *Handlers) handleCallback(c *gin.Context, redirectURI string, done func(token, cliSession string)) {
	stateStr := c.Query("state")
	code := c.Query("code")

	stateClaims, err := h.parseStateJWT(stateStr)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid state")
		return
	}

	oauthToken, err := h.svc.ExchangeCode(c.Request.Context(), code, redirectURI)
	if err != nil {
		c.String(http.StatusBadGateway, "code exchange failed")
		return
	}

	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok {
		c.String(http.StatusBadGateway, "missing id_token")
		return
	}

	idToken, err := h.svc.VerifyIDToken(c.Request.Context(), rawIDToken, stateClaims.Nonce)
	if err != nil {
		c.String(http.StatusUnauthorized, "id_token verification failed")
		return
	}

	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		c.String(http.StatusInternalServerError, "failed to parse claims")
		return
	}
	userName := claims.PreferredUsername
	if userName == "" {
		userName = claims.Email
	}

	user, err := db.FindOrCreate(h.db, userName, claims.Email, db.AuthSourceOIDC)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to find/create user")
		return
	}

	appToken, err := h.issueAppToken(user.ID, user.UserName)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to issue token")
		return
	}

	done(appToken, stateClaims.CLISession)
}

func cliRedisKey(sessionID string) string {
	return "cli_login_session:" + sessionID
}
