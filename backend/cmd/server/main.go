// @title           Platform Backend API
// @version         1.0
// @description     REST API for managing tasks and sandboxes.
// @host            localhost:8080
// @BasePath        /

package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/go-redis/redis/v8"
	"github.com/your-org/platform-backend/internal/api"
	"github.com/your-org/platform-backend/internal/sandbox"
	"github.com/your-org/platform-backend/internal/storage"
	"github.com/your-org/platform-backend/internal/task"
	"github.com/your-org/platform-backend/pkg/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

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
	} {
		if v != "" {
			baseEnv[k] = v
		}
	}

	var platform *sandbox.PlatformSpec
	if p := cfg.Sandbox.Platform; p != nil && p.OS != "" && p.Arch != "" {
		platform = &sandbox.PlatformSpec{OS: p.OS, Arch: p.Arch}
	}

	var ofsClient storage.OFSClient
	if cfg.OrangeFS.Endpoint != "" {
		c, err := storage.New(cfg.OrangeFS.Endpoint, cfg.OrangeFS.Volume, cfg.OrangeFS.AccessKey, cfg.OrangeFS.SecretKey)
		if err != nil {
			log.Fatalf("creating OFS client: %v", err)
		}
		ofsClient = c
		log.Printf("OFS client configured: endpoint=%s volume=%s", cfg.OrangeFS.Endpoint, cfg.OrangeFS.Volume)
	}

	var repo task.Repository
	if cfg.Redis.URL != "" {
		opt, err := redis.ParseURL(cfg.Redis.URL)
		if err != nil {
			log.Fatalf("parse redis URL: %v", err)
		}
		rdb := redis.NewClient(opt)
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			log.Fatalf("redis ping: %v", err)
		}
		repo = task.NewRedisRepository(rdb)
		log.Printf("task store: Redis at %s", cfg.Redis.URL)
	} else {
		repo = task.NewMemoryRepository()
		log.Printf("task store: in-memory (set redis.url in config to enable persistence)")
	}

	mgr := sandbox.NewManager(cfg.Sandbox.ServerURL, cfg.Sandbox.APIKey, baseEnv, cfg.Sandbox.Image, platform)
	router := api.NewRouter(repo, mgr, cfg.Server.CORSOrigin, ofsClient)

	log.Printf("listening on :%s", cfg.Server.Port)
	if err := http.ListenAndServe(":"+cfg.Server.Port, router); err != nil {
		log.Fatal(err)
	}
}
