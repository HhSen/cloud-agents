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
	"github.com/your-org/platform-backend/internal/auth"
	"github.com/your-org/platform-backend/internal/db"
	"github.com/your-org/platform-backend/pkg/config"
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
func (h *Handlers) Callback(c *gin.Context) {
	h.handleCallback(c, h.oidc.RedirectURI, func(token, _ string) {
		target := fmt.Sprintf("%s/login/oidc#access_token=%s", h.cfg.FrontendURL, token)
		c.Redirect(http.StatusFound, target)
	})
}

// CLILogin creates a pending Redis session and returns the browser auth URL.
func (h *Handlers) CLILogin(c *gin.Context) {
	var body struct {
		SessionID string `json:"session_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id required"})
		return
	}

	nonce := uuid.New().String()
	stateJWT, err := h.buildStateJWT(nonce, "", body.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build state"})
		return
	}

	sess := cliSession{Status: "pending"}
	data, _ := json.Marshal(sess)
	key := cliRedisKey(body.SessionID)
	if err := h.redis.Set(c.Request.Context(), key, data, 5*time.Minute).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	authURL, err := h.svc.AuthURL(c.Request.Context(), h.oidc.CLIRedirectURI, stateJWT, nonce)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build auth URL"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"auth_url": authURL})
}

// CLICallback handles the OIDC redirect for the CLI flow; writes the app JWT to Redis.
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
func (h *Handlers) CLIPoll(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id required"})
		return
	}
	key := cliRedisKey(sessionID)
	data, err := h.redis.Get(c.Request.Context(), key).Bytes()
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	var sess cliSession
	if err := json.Unmarshal(data, &sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid session data"})
		return
	}
	if sess.Status == "completed" {
		h.redis.Del(context.Background(), key)
		c.JSON(http.StatusOK, gin.H{"status": "completed", "token": sess.Token})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": sess.Status})
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
