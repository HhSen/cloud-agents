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
	Redis     RedisConfig     `yaml:"redis"`
	MySQL     MySQLConfig     `yaml:"mysql"`
	Auth      AuthConfig      `yaml:"auth"`
	OIDC      OIDCConfig      `yaml:"oidc"`
	SSO       SSOConfig       `yaml:"sso"`
	Security  SecurityConfig  `yaml:"security"`
	Log       LogConfig       `yaml:"log"`
}

type SecurityConfig struct {
	SSHKeySecret string `yaml:"ssh_key_secret"` // 32-byte hex; generate with: openssl rand -hex 32
}

type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error (default: info)
	Format string `yaml:"format"` // json | text (default: json)
}

type ServerConfig struct {
	Port       string `yaml:"port"`
	CORSOrigin string `yaml:"cors_origin"`
}

type SandboxConfig struct {
	ServerURL      string          `yaml:"server_url"`
	APIKey         string          `yaml:"api_key"`
	Image          string          `yaml:"image"`
	Platform       *PlatformConfig `yaml:"platform"`
	MemoryLimit    string          `yaml:"memory_limit"`    // e.g. "4Gi"; defaults to "4Gi" if empty
	CPULimit       string          `yaml:"cpu_limit"`       // e.g. "2000m"; defaults to "2000m" if empty
	TimeoutSeconds int             `yaml:"timeout_seconds"` // sandbox TTL in seconds; defaults to 3600
}

type PlatformConfig struct {
	OS   string `yaml:"os"`
	Arch string `yaml:"arch"`
}

type AnthropicConfig struct {
	APIKey                   string `yaml:"api_key"`
	BaseURL                  string `yaml:"base_url"`
	Model                    string `yaml:"model"`
	DisableExperimentalBetas string `yaml:"disable_experimental_betas"`
}

type RedisConfig struct {
	URL string `yaml:"url"` // e.g. redis://localhost:6379; empty = use in-memory store
}

type MySQLConfig struct {
	DSN string `yaml:"dsn"`
}

type AuthConfig struct {
	SecretKey       string `yaml:"secret_key"`
	OIDCStateSecret string `yaml:"oidc_state_secret"`
	TokenTTLSeconds int    `yaml:"token_ttl_seconds"`
	StateTTLSeconds int    `yaml:"state_ttl_seconds"`
	FrontendURL     string `yaml:"frontend_url"`
}

type OIDCConfig struct {
	ClientID       string `yaml:"client_id"`
	ClientSecret   string `yaml:"client_secret"`
	DiscoveryURL   string `yaml:"discovery_url"`
	RedirectURI    string `yaml:"redirect_uri"`
	CLIRedirectURI string `yaml:"cli_redirect_uri"`
}

type SSOConfig struct {
	BaseURL     string `yaml:"base_url"`
	AppID       string `yaml:"app_id"`
	AppKey      string `yaml:"app_key"`
	CallbackURL string `yaml:"callback_url"`
}

type OrangeFSConfig struct {
	Addr      string `yaml:"addr"`     // injected into sandbox as ORANGEFS_RS_ADDR
	Token     string `yaml:"token"`    // injected into sandbox as ORANGEFS_TOKEN
	Endpoint  string `yaml:"endpoint"` // public S3 endpoint URL for the backend client
	Volume    string `yaml:"volume"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

func Load(path string) (*Config, error) {
	// Defaults
	cfg := Config{
		Server: ServerConfig{
			Port:       "8091",
			CORSOrigin: "http://localhost:5173",
		},
		Sandbox: SandboxConfig{
			ServerURL:      "http://localhost:8080",
			Image:          "opensandbox/code-interpreter:local",
			TimeoutSeconds: 3600,
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
