package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/your-org/platform-backend/internal/api"
	"github.com/your-org/platform-backend/internal/conversation"
	"github.com/your-org/platform-backend/internal/sandbox"
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

	store := conversation.NewStore()
	mgr := sandbox.NewManager(cfg.Sandbox.ServerURL, cfg.Sandbox.APIKey, baseEnv, cfg.Sandbox.Image, platform)
	router := api.NewRouter(store, mgr, cfg.Server.CORSOrigin)

	log.Printf("listening on :%s", cfg.Server.Port)
	if err := http.ListenAndServe(":"+cfg.Server.Port, router); err != nil {
		log.Fatal(err)
	}
}
