package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const defaultBaseURL = "https://api.x.ai/v1"

type Config struct {
	APIKey       string                     `json:"api_key,omitempty"`
	BaseURL      string                     `json:"base_url,omitempty"`
	Model        string                     `json:"model,omitempty"`
	Backend      string                     `json:"backend,omitempty"`
	SystemPrompt string                     `json:"system_prompt,omitempty"`
	MaxSteps     int                        `json:"max_steps,omitempty"`
	MCPServers   map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	LSPServers   map[string]LSPServerConfig `json:"lsp_servers,omitempty"`
	HTTPTimeout  time.Duration              `json:"-"`
}

type MCPServerConfig struct {
	Command string            `json:"command" toml:"command"`
	Args    []string          `json:"args,omitempty" toml:"args"`
	Env     map[string]string `json:"env,omitempty" toml:"env"`
	URL     string            `json:"url,omitempty" toml:"url"`
	Headers map[string]string `json:"headers,omitempty" toml:"headers"`
	Enabled *bool             `json:"enabled,omitempty" toml:"enabled"`
}

type LSPServerConfig struct {
	Command    string            `json:"command" toml:"command"`
	Args       []string          `json:"args,omitempty" toml:"args"`
	Env        map[string]string `json:"env,omitempty" toml:"env"`
	Extensions []string          `json:"extensions,omitempty" toml:"extensions"`
	Enabled    *bool             `json:"enabled,omitempty" toml:"enabled"`
}

func (c LSPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type fileConfig struct {
	APIKey       string                     `json:"api_key,omitempty" toml:"api_key"`
	BaseURL      string                     `json:"base_url,omitempty" toml:"base_url"`
	Model        string                     `json:"model,omitempty" toml:"model_name"`
	Backend      string                     `json:"backend,omitempty" toml:"backend"`
	SystemPrompt string                     `json:"system_prompt,omitempty" toml:"system_prompt"`
	MaxSteps     int                        `json:"max_steps,omitempty" toml:"max_steps"`
	HTTPTimeout  string                     `json:"http_timeout,omitempty" toml:"http_timeout"`
	MCPServers   map[string]MCPServerConfig `json:"mcp_servers,omitempty" toml:"mcp_servers"`
	LSPServers   map[string]LSPServerConfig `json:"lsp_servers,omitempty" toml:"lsp_servers"`
	Models       struct {
		Default string `toml:"default"`
	} `json:"-" toml:"models"`
	ModelEntries map[string]modelConfig `json:"-" toml:"model"`
}

type modelConfig struct {
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
	Backend string `toml:"backend"`
	EnvKey  any    `toml:"env_key"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		BaseURL:     defaultBaseURL,
		Backend:     "responses",
		MaxSteps:    20,
		HTTPTimeout: 10 * time.Minute,
	}
	if path == "" {
		var err error
		path, err = discoverDefaultPath()
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
		if err := unmarshalConfig(path, data, &disk); err != nil {
			return Config{}, fmt.Errorf("parse config %q: %w", path, err)
		}
		applyModelConfig(&disk)
		if disk.APIKey != "" {
			cfg.APIKey = disk.APIKey
		}
		if disk.BaseURL != "" {
			cfg.BaseURL = disk.BaseURL
		}
		if disk.Model != "" {
			cfg.Model = disk.Model
		}
		if disk.Backend != "" {
			cfg.Backend = disk.Backend
		}
		if disk.SystemPrompt != "" {
			cfg.SystemPrompt = disk.SystemPrompt
		}
		if disk.MaxSteps > 0 {
			cfg.MaxSteps = disk.MaxSteps
		}
		cfg.MCPServers = disk.MCPServers
		cfg.LSPServers = disk.LSPServers
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
	if value := os.Getenv("GORK_BACKEND"); value != "" {
		cfg.Backend = value
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".grok", "config.toml"), nil
}

func discoverDefaultPath() (string, error) {
	legacy, err := DefaultPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	configDir, err := os.UserConfigDir()
	if err == nil {
		jsonPath := filepath.Join(configDir, "gork-go", "config.json")
		if _, statErr := os.Stat(jsonPath); statErr == nil {
			return jsonPath, nil
		}
	}
	return legacy, nil
}

func unmarshalConfig(path string, data []byte, target *fileConfig) error {
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		return toml.Unmarshal(data, target)
	}
	if err := json.Unmarshal(data, target); err == nil {
		return nil
	}
	return toml.Unmarshal(data, target)
}

func applyModelConfig(disk *fileConfig) {
	selected := disk.Models.Default
	if disk.Model == "" && selected != "" {
		disk.Model = selected
	}
	entry, ok := disk.ModelEntries[selected]
	if !ok {
		return
	}
	if entry.Model != "" {
		disk.Model = entry.Model
	}
	if entry.BaseURL != "" {
		disk.BaseURL = entry.BaseURL
	}
	if entry.APIKey != "" {
		disk.APIKey = entry.APIKey
	} else if key := firstConfiguredEnv(entry.EnvKey); key != "" {
		disk.APIKey = key
	}
	if entry.Backend != "" {
		disk.Backend = entry.Backend
	}
}

func firstConfiguredEnv(value any) string {
	var names []string
	switch typed := value.(type) {
	case string:
		names = []string{typed}
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
	case []string:
		names = typed
	}
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func (c Config) Validate() error {
	if c.APIKey == "" {
		return errors.New("missing API key: set GORK_API_KEY or XAI_API_KEY")
	}
	if c.Model == "" {
		return errors.New("missing model: pass --model or set GORK_MODEL")
	}
	if c.Backend != "responses" && c.Backend != "chat_completions" && c.Backend != "anthropic_messages" {
		return fmt.Errorf("unsupported backend %q: use responses, chat_completions, or anthropic_messages", c.Backend)
	}
	if c.BaseURL == "" {
		return errors.New("missing API base URL")
	}
	if c.MaxSteps < 1 {
		return errors.New("max steps must be greater than zero")
	}
	return nil
}
