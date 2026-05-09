package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Sandbox   SandboxConfig   `yaml:"sandbox"`
	Anthropic AnthropicConfig `yaml:"anthropic"`
	OrangeFS  OrangeFSConfig  `yaml:"orangefs"`
}

type ServerConfig struct {
	Port       string `yaml:"port"`
	CORSOrigin string `yaml:"cors_origin"`
}

type SandboxConfig struct {
	ServerURL string          `yaml:"server_url"`
	APIKey    string          `yaml:"api_key"`
	Image     string          `yaml:"image"`
	Platform  *PlatformConfig `yaml:"platform"`
}

type PlatformConfig struct {
	OS   string `yaml:"os"`
	Arch string `yaml:"arch"`
}

type AnthropicConfig struct {
	APIKey               string `yaml:"api_key"`
	BaseURL              string `yaml:"base_url"`
	Model                string `yaml:"model"`
	DisableExperimentalBetas string `yaml:"disable_experimental_betas"`
}

type OrangeFSConfig struct {
	Addr   string `yaml:"addr"`
	Volume string `yaml:"volume"`
}

func Load(path string) (*Config, error) {
	// Defaults
	cfg := Config{
		Server: ServerConfig{
			Port:       "8081",
			CORSOrigin: "http://localhost:5173",
		},
		Sandbox: SandboxConfig{
			ServerURL: "http://localhost:8080",
			Image:     "opensandbox/claude-agent-server:latest",
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	return &cfg, nil
}
