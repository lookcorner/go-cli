package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.x.ai/v1"

type Config struct {
	APIKey       string                     `json:"api_key,omitempty"`
	BaseURL      string                     `json:"base_url,omitempty"`
	Model        string                     `json:"model,omitempty"`
	SystemPrompt string                     `json:"system_prompt,omitempty"`
	MaxSteps     int                        `json:"max_steps,omitempty"`
	MCPServers   map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	HTTPTimeout  time.Duration              `json:"-"`
}

type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type fileConfig struct {
	APIKey       string                     `json:"api_key,omitempty"`
	BaseURL      string                     `json:"base_url,omitempty"`
	Model        string                     `json:"model,omitempty"`
	SystemPrompt string                     `json:"system_prompt,omitempty"`
	MaxSteps     int                        `json:"max_steps,omitempty"`
	HTTPTimeout  string                     `json:"http_timeout,omitempty"`
	MCPServers   map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		BaseURL:     defaultBaseURL,
		MaxSteps:    20,
		HTTPTimeout: 10 * time.Minute,
	}
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if err == nil {
		var disk fileConfig
		if err := json.Unmarshal(data, &disk); err != nil {
			return Config{}, fmt.Errorf("parse config %q: %w", path, err)
		}
		if disk.APIKey != "" {
			cfg.APIKey = disk.APIKey
		}
		if disk.BaseURL != "" {
			cfg.BaseURL = disk.BaseURL
		}
		if disk.Model != "" {
			cfg.Model = disk.Model
		}
		if disk.SystemPrompt != "" {
			cfg.SystemPrompt = disk.SystemPrompt
		}
		if disk.MaxSteps > 0 {
			cfg.MaxSteps = disk.MaxSteps
		}
		cfg.MCPServers = disk.MCPServers
		if disk.HTTPTimeout != "" {
			d, err := time.ParseDuration(disk.HTTPTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse http_timeout: %w", err)
			}
			cfg.HTTPTimeout = d
		}
	}

	applyEnv(&cfg)
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if value := firstEnv("GORK_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	if value := firstEnv("GORK_BASE_URL", "XAI_BASE_URL", "OPENAI_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := os.Getenv("GORK_MODEL"); value != "" {
		cfg.Model = value
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "gork-go", "config.json"), nil
}

func (c Config) Validate() error {
	if c.APIKey == "" {
		return errors.New("missing API key: set GORK_API_KEY or XAI_API_KEY")
	}
	if c.Model == "" {
		return errors.New("missing model: pass --model or set GORK_MODEL")
	}
	if c.BaseURL == "" {
		return errors.New("missing API base URL")
	}
	if c.MaxSteps < 1 {
		return errors.New("max steps must be greater than zero")
	}
	return nil
}
