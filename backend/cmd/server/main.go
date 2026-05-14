// @title           Platform Backend API
// @version         1.0
// @description     REST API for managing tasks and sandboxes.
// @host            localhost:8080
// @BasePath        /

package main

import (
	"context"
	"flag"
	"net/http"
	"os"

	"github.com/go-redis/redis/v8"
	"github.com/your-org/platform-backend/internal/api"
	"github.com/your-org/platform-backend/internal/db"
	oidcpkg "github.com/your-org/platform-backend/internal/oidc"
	ssopkg "github.com/your-org/platform-backend/internal/sso"
	"github.com/your-org/platform-backend/internal/sandbox"
	"github.com/your-org/platform-backend/internal/storage"
	"github.com/your-org/platform-backend/internal/task"
	"github.com/your-org/platform-backend/pkg/config"
	"github.com/your-org/platform-backend/pkg/logger"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Default().Error("loading config", "err", err)
		os.Exit(1)
	}

	logger.Init(logger.Config{Level: cfg.Log.Level, Format: cfg.Log.Format})

	baseEnv := map[string]string{
		"ANTHROPIC_API_KEY": cfg.Anthropic.APIKey,
		"PORT":              "3000",
	}
	for k, v := range map[string]string{
		"ANTHROPIC_BASE_URL":                     cfg.Anthropic.BaseURL,
		"ANTHROPIC_MODEL":                        cfg.Anthropic.Model,
		"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": cfg.Anthropic.DisableExperimentalBetas,
		"ORANGEFS_RS_ADDR":                       cfg.OrangeFS.Addr,
		"ORANGEFS_TOKEN":                         cfg.OrangeFS.Token,
		"ORANGEFS_VOLUME":                        cfg.OrangeFS.Volume,
		"ORANGEFS_ENDPOINT":                      cfg.OrangeFS.Endpoint,
		"S3_ACCESS_KEY":                          cfg.OrangeFS.AccessKey,
		"S3_SECRET_KEY":                          cfg.OrangeFS.SecretKey,
	} {
		if v != "" {
			baseEnv[k] = v
		}
	}

	var platform *sandbox.PlatformSpec
	if p := cfg.Sandbox.Platform; p != nil && p.OS != "" && p.Arch != "" {
		platform = &sandbox.PlatformSpec{OS: p.OS, Arch: p.Arch}
	}

	var ofsClient *storage.Client
	if cfg.OrangeFS.Endpoint != "" {
		c, err := storage.New(cfg.OrangeFS.Endpoint, cfg.OrangeFS.Volume, cfg.OrangeFS.AccessKey, cfg.OrangeFS.SecretKey)
		if err != nil {
			logger.Default().Error("creating OFS client", "err", err)
			os.Exit(1)
		}
		ofsClient = c
		logger.Default().Info("OFS client configured", "endpoint", cfg.OrangeFS.Endpoint, "volume", cfg.OrangeFS.Volume)
	}

	var repo task.Repository
	var rdb *redis.Client
	if cfg.Redis.URL != "" {
		opt, err := redis.ParseURL(cfg.Redis.URL)
		if err != nil {
			logger.Default().Error("parse redis URL", "err", err)
			os.Exit(1)
		}
		rdb = redis.NewClient(opt)
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			logger.Default().Error("redis ping failed", "err", err)
			os.Exit(1)
		}
		logger.Default().Info("Redis connected", "url", cfg.Redis.URL)
	}

	if cfg.MySQL.DSN == "" {
		logger.Default().Error("mysql.dsn is required")
		os.Exit(1)
	}
	if cfg.Auth.SecretKey == "" {
		logger.Default().Error("auth.secret_key must be set")
		os.Exit(1)
	}
	if cfg.Auth.TokenTTLSeconds <= 0 {
		logger.Default().Error("auth.token_ttl_seconds must be > 0")
		os.Exit(1)
	}
	gormDB, err := db.Open(cfg.MySQL.DSN)
	if err != nil {
		logger.Default().Error("open mysql", "err", err)
		os.Exit(1)
	}
	logger.Default().Info("MySQL connected")

	if rdb == nil {
		logger.Default().Error("redis.url is required", "reason", "Redis holds ephemeral sandbox mappings and distributed locks")
		os.Exit(1)
	}
	repo = task.NewMySQLRepository(gormDB, rdb)
	logger.Default().Info("task store: MySQL + Redis")

	var oidcSvc *oidcpkg.Service
	if cfg.OIDC.ClientID != "" && cfg.OIDC.DiscoveryURL != "" {
		if cfg.Auth.OIDCStateSecret == "" {
			logger.Default().Error("auth.oidc_state_secret must be set when OIDC is enabled")
			os.Exit(1)
		}
		if cfg.Auth.StateTTLSeconds <= 0 {
			logger.Default().Error("auth.state_ttl_seconds must be > 0 when OIDC is enabled")
			os.Exit(1)
		}
		oidcSvc = oidcpkg.New(cfg.OIDC)
		logger.Default().Info("OIDC enabled", "discovery_url", cfg.OIDC.DiscoveryURL)
	}

	var ssoSvc *ssopkg.Service
	if cfg.SSO.AppID != "" {
		ssoSvc = ssopkg.New(cfg.SSO)
		logger.Default().Info("SSO enabled", "base_url", cfg.SSO.BaseURL)
	}

	if cfg.Security.SSHKeySecret == "" {
		var count int64
		gormDB.Model(&db.User{}).Where("ssh_private_key_enc != ''").Count(&count)
		if count > 0 {
			logger.Default().Error("security.ssh_key_secret must be set — some users have stored SSH keys")
			os.Exit(1)
		}
	}

	mgr := sandbox.NewManager(cfg.Sandbox.ServerURL, cfg.Sandbox.APIKey, baseEnv, cfg.Sandbox.Image, platform, cfg.Sandbox.MemoryLimit, cfg.Sandbox.CPULimit)

	kindsRepo := db.NewKindsRepository(gormDB)
	if ofsClient != nil {
		mgr.WithResources(kindsRepo, ofsClient)
	}
	if cfg.Security.SSHKeySecret != "" {
		mgr.WithSSHKeys(gormDB, cfg.Security.SSHKeySecret)
	}

	router := api.NewRouter(api.RouterDeps{
		Store:           repo,
		Manager:         mgr,
		FileStore:       ofsClient,
		KindsRepo:       kindsRepo,
		OFSWriter:       ofsClient,
		OFSReader:       ofsClient,
		WorkspaceReader: ofsClient,
		CORSOrigin:      cfg.Server.CORSOrigin,
		DB:              gormDB,
		Redis:           rdb,
		Cfg:             cfg,
		OIDCService:     oidcSvc,
		SSOService:      ssoSvc,
	})

	logger.Default().Info("listening", "addr", ":"+cfg.Server.Port)
	if err := http.ListenAndServe(":"+cfg.Server.Port, router); err != nil {
		logger.Default().Error("server failed", "err", err)
		os.Exit(1)
	}
}
