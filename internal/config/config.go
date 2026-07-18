package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/pelletier/go-toml/v2"
)

const defaultBaseURL = "https://api.x.ai/v1"

type Config struct {
	APIKey                      string                     `json:"api_key,omitempty"`
	BaseURL                     string                     `json:"base_url,omitempty"`
	Model                       string                     `json:"model,omitempty"`
	Backend                     string                     `json:"backend,omitempty"`
	SystemPrompt                string                     `json:"system_prompt,omitempty"`
	MaxSteps                    int                        `json:"max_steps,omitempty"`
	MCPServers                  map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	LSPServers                  map[string]LSPServerConfig `json:"lsp_servers,omitempty"`
	Permission                  PermissionConfig           `json:"permission,omitempty"`
	ContextWindow               int                        `json:"context_window,omitempty"`
	AutoCompactThresholdPercent int                        `json:"auto_compact_threshold_percent,omitempty"`
	Pruning                     PruningConfig              `json:"pruning"`
	Compat                      compat.Config              `json:"compat"`
	HTTPTimeout                 time.Duration              `json:"-"`
	WebSearch                   WebSearchConfig            `json:"web_search,omitempty"`
	WebFetch                    WebFetchConfig             `json:"web_fetch,omitempty"`
}

type WebSearchConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

type WebFetchConfig struct {
	ProxyEndpoint     string   `json:"proxy_endpoint,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	ProxyConfigured   bool     `json:"-"`
	DomainsConfigured bool     `json:"-"`
}

type PermissionConfig struct {
	Rules []PermissionRule `json:"rules,omitempty" toml:"rules"`
}

type PermissionRule struct {
	Action      string  `json:"action" toml:"action"`
	Tool        string  `json:"tool,omitempty" toml:"tool"`
	Pattern     *string `json:"pattern,omitempty" toml:"pattern"`
	PatternMode string  `json:"pattern_mode,omitempty" toml:"pattern_mode"`
}

type PruningConfig struct {
	Enabled           bool `json:"enabled"`
	KeepLastNTurns    int  `json:"keep_last_n_turns"`
	SoftTrimThreshold int  `json:"soft_trim_threshold"`
	SoftTrimHead      int  `json:"soft_trim_head"`
	SoftTrimTail      int  `json:"soft_trim_tail"`
	HardClearAgeTurns int  `json:"hard_clear_age_turns"`
}

type MCPServerConfig struct {
	Command string            `json:"command" toml:"command"`
	Args    []string          `json:"args,omitempty" toml:"args"`
	Env     map[string]string `json:"env,omitempty" toml:"env"`
	URL     string            `json:"url,omitempty" toml:"url"`
	Type    string            `json:"type,omitempty" toml:"type"`
	Headers map[string]string `json:"headers,omitempty" toml:"headers"`
	Enabled *bool             `json:"enabled,omitempty" toml:"enabled"`
}

type LSPServerConfig struct {
	Command               string            `json:"command" toml:"command"`
	Transport             string            `json:"transport,omitempty" toml:"transport"`
	Args                  []string          `json:"args,omitempty" toml:"args"`
	Env                   map[string]string `json:"env,omitempty" toml:"env"`
	Extensions            []string          `json:"extensions,omitempty" toml:"extensions"`
	InitializationOptions map[string]any    `json:"initialization_options,omitempty" toml:"initialization_options"`
	Settings              map[string]any    `json:"settings,omitempty" toml:"settings"`
	Enabled               *bool             `json:"enabled,omitempty" toml:"enabled"`
}

func (c LSPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type fileConfig struct {
	APIKey        string                     `json:"api_key,omitempty" toml:"api_key"`
	BaseURL       string                     `json:"base_url,omitempty" toml:"base_url"`
	Model         string                     `json:"model,omitempty" toml:"model_name"`
	Backend       string                     `json:"backend,omitempty" toml:"backend"`
	SystemPrompt  string                     `json:"system_prompt,omitempty" toml:"system_prompt"`
	MaxSteps      int                        `json:"max_steps,omitempty" toml:"max_steps"`
	HTTPTimeout   string                     `json:"http_timeout,omitempty" toml:"http_timeout"`
	MCPServers    map[string]MCPServerConfig `json:"mcp_servers,omitempty" toml:"mcp_servers"`
	LSPServers    map[string]LSPServerConfig `json:"lsp_servers,omitempty" toml:"lsp_servers"`
	Permission    PermissionConfig           `json:"permission,omitempty" toml:"permission"`
	Session       sessionConfig              `json:"session,omitempty" toml:"session"`
	ContextWindow int                        `json:"context_window,omitempty" toml:"context_window"`
	Compaction    fileCompactionConfig       `json:"compaction,omitempty" toml:"compaction"`
	Compat        fileCompatConfig           `json:"compat,omitempty" toml:"compat"`
	Models        struct {
		Default   string `toml:"default"`
		WebSearch string `toml:"web_search"`
	} `json:"-" toml:"models"`
	ModelEntries map[string]modelConfig `json:"-" toml:"model"`
	Toolset      struct {
		WebFetch fileWebFetchConfig `json:"web_fetch,omitempty" toml:"web_fetch"`
	} `json:"toolset,omitempty" toml:"toolset"`
}

type fileWebFetchConfig struct {
	ProxyEndpoint  *string  `json:"proxy_endpoint,omitempty" toml:"proxy_endpoint"`
	AllowedDomains []string `json:"allowed_domains,omitempty" toml:"allowed_domains"`
}

type modelConfig struct {
	Model                       string `toml:"model"`
	BaseURL                     string `toml:"base_url"`
	APIKey                      string `toml:"api_key"`
	Backend                     string `toml:"backend"`
	EnvKey                      any    `toml:"env_key"`
	ContextWindow               int    `toml:"context_window"`
	AutoCompactThresholdPercent *int   `toml:"auto_compact_threshold_percent"`
}

type sessionConfig struct {
	AutoCompactThresholdPercent *int `json:"auto_compact_threshold_percent,omitempty" toml:"auto_compact_threshold_percent"`
}

type fileCompactionConfig struct {
	Pruning filePruningConfig `json:"pruning,omitempty" toml:"pruning"`
}

type filePruningConfig struct {
	Enabled           *bool `json:"enabled,omitempty" toml:"enabled"`
	KeepLastNTurns    *int  `json:"keep_last_n_turns,omitempty" toml:"keep_last_n_turns"`
	SoftTrimThreshold *int  `json:"soft_trim_threshold,omitempty" toml:"soft_trim_threshold"`
	SoftTrimHead      *int  `json:"soft_trim_head,omitempty" toml:"soft_trim_head"`
	SoftTrimTail      *int  `json:"soft_trim_tail,omitempty" toml:"soft_trim_tail"`
	HardClearAgeTurns *int  `json:"hard_clear_age_turns,omitempty" toml:"hard_clear_age_turns"`
}

type fileVendorCompat struct {
	Skills *bool `json:"skills,omitempty" toml:"skills"`
	Rules  *bool `json:"rules,omitempty" toml:"rules"`
	Agents *bool `json:"agents,omitempty" toml:"agents"`
}

type fileCompatConfig struct {
	Cursor fileVendorCompat `json:"cursor,omitempty" toml:"cursor"`
	Claude fileVendorCompat `json:"claude,omitempty" toml:"claude"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		BaseURL:                     defaultBaseURL,
		Backend:                     "responses",
		MaxSteps:                    20,
		HTTPTimeout:                 10 * time.Minute,
		ContextWindow:               131072,
		AutoCompactThresholdPercent: 85,
		Compat:                      compat.Default(),
		Pruning:                     PruningConfig{Enabled: true, KeepLastNTurns: 3, SoftTrimThreshold: 4000, SoftTrimHead: 1500, SoftTrimTail: 1500, HardClearAgeTurns: 10},
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
		if disk.Models.WebSearch != "" {
			entry, ok := disk.ModelEntries[disk.Models.WebSearch]
			if !ok {
				return Config{}, fmt.Errorf("web search model %q is not defined", disk.Models.WebSearch)
			}
			if entry.Backend != "" && entry.Backend != "responses" {
				return Config{}, errors.New("web search model backend must be responses")
			}
			cfg.WebSearch = WebSearchConfig{
				Enabled: true, APIKey: entry.APIKey, BaseURL: entry.BaseURL, Model: entry.Model,
			}
			if cfg.WebSearch.APIKey == "" {
				cfg.WebSearch.APIKey = firstConfiguredEnv(entry.EnvKey)
			}
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
		cfg.Permission = disk.Permission
		if disk.Toolset.WebFetch.ProxyEndpoint != nil {
			cfg.WebFetch.ProxyEndpoint = *disk.Toolset.WebFetch.ProxyEndpoint
			cfg.WebFetch.ProxyConfigured = true
		}
		if disk.Toolset.WebFetch.AllowedDomains != nil {
			cfg.WebFetch.AllowedDomains = append([]string(nil), disk.Toolset.WebFetch.AllowedDomains...)
			cfg.WebFetch.DomainsConfigured = true
		}
		if disk.ContextWindow > 0 {
			cfg.ContextWindow = disk.ContextWindow
		}
		if disk.Session.AutoCompactThresholdPercent != nil {
			cfg.AutoCompactThresholdPercent = *disk.Session.AutoCompactThresholdPercent
		}
		applyPruningConfig(&cfg.Pruning, disk.Compaction.Pruning)
		applyCompatConfig(&cfg.Compat, disk.Compat)
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
	cfg.WebSearch.BaseURL = strings.TrimRight(cfg.WebSearch.BaseURL, "/")
	return cfg, nil
}

func applyCompatConfig(target *compat.Config, source fileCompatConfig) {
	applyVendorCompat(&target.Cursor, source.Cursor)
	applyVendorCompat(&target.Claude, source.Claude)
}

func applyVendorCompat(target *compat.Vendor, source fileVendorCompat) {
	if source.Skills != nil {
		target.Skills = *source.Skills
	}
	if source.Rules != nil {
		target.Rules = *source.Rules
	}
	if source.Agents != nil {
		target.Agents = *source.Agents
	}
}

func applyPruningConfig(target *PruningConfig, source filePruningConfig) {
	if source.Enabled != nil {
		target.Enabled = *source.Enabled
	}
	if source.KeepLastNTurns != nil {
		target.KeepLastNTurns = *source.KeepLastNTurns
	}
	if source.SoftTrimThreshold != nil {
		target.SoftTrimThreshold = *source.SoftTrimThreshold
	}
	if source.SoftTrimHead != nil {
		target.SoftTrimHead = *source.SoftTrimHead
	}
	if source.SoftTrimTail != nil {
		target.SoftTrimTail = *source.SoftTrimTail
	}
	if source.HardClearAgeTurns != nil {
		target.HardClearAgeTurns = *source.HardClearAgeTurns
	}
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
	if value := os.Getenv("GORK_WEB_SEARCH_API_KEY"); value != "" {
		cfg.WebSearch.Enabled = true
		cfg.WebSearch.APIKey = value
	}
	if value := os.Getenv("GORK_WEB_SEARCH_BASE_URL"); value != "" {
		cfg.WebSearch.Enabled = true
		cfg.WebSearch.BaseURL = value
	}
	if value := os.Getenv("GORK_WEB_SEARCH_MODEL"); value != "" {
		cfg.WebSearch.Enabled = true
		cfg.WebSearch.Model = value
	}
	if !cfg.WebFetch.ProxyConfigured {
		if value := os.Getenv("GROK_WEB_FETCH_PROXY"); value != "" {
			cfg.WebFetch.ProxyEndpoint = value
			cfg.WebFetch.ProxyConfigured = true
		}
	}
	if value := os.Getenv("GROK_AUTO_COMPACT_THRESHOLD_PERCENT"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 && parsed <= 100 {
			cfg.AutoCompactThresholdPercent = parsed
		}
	}
	if value := os.Getenv("GROK_DEBUG_CONTEXT_WINDOW"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			cfg.ContextWindow = parsed
		}
	}
	applyCompatEnv(&cfg.Compat.Cursor, "CURSOR")
	applyCompatEnv(&cfg.Compat.Claude, "CLAUDE")
}

func (c Config) WebSearchEndpoint() (WebSearchConfig, bool) {
	search := c.WebSearch
	if !search.Enabled {
		if c.Backend != "responses" {
			return WebSearchConfig{}, false
		}
		search.Enabled = true
	}
	if search.APIKey == "" {
		search.APIKey = c.APIKey
	}
	if c.Backend == "responses" {
		if search.BaseURL == "" {
			search.BaseURL = c.BaseURL
		}
		if search.Model == "" {
			search.Model = c.Model
		}
	}
	search.BaseURL = strings.TrimRight(search.BaseURL, "/")
	return search, true
}

func applyCompatEnv(target *compat.Vendor, vendor string) {
	for surface, field := range map[string]*bool{
		"SKILLS": &target.Skills,
		"RULES":  &target.Rules,
		"AGENTS": &target.Agents,
	} {
		if value, ok := envBool("GROK_" + vendor + "_" + surface + "_ENABLED"); ok {
			*field = value
		}
	}
}

func envBool(name string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled":
		return false, true
	default:
		return false, false
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
	if entry.ContextWindow > 0 {
		disk.ContextWindow = entry.ContextWindow
	}
	if entry.AutoCompactThresholdPercent != nil {
		disk.Session.AutoCompactThresholdPercent = entry.AutoCompactThresholdPercent
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
	if search, enabled := c.WebSearchEndpoint(); enabled && (search.APIKey == "" || search.BaseURL == "" || search.Model == "") {
		return errors.New("web search requires an API key, base URL, and model")
	}
	if c.MaxSteps < 1 {
		return errors.New("max steps must be greater than zero")
	}
	if c.ContextWindow < 1 {
		return errors.New("context window must be greater than zero")
	}
	if c.AutoCompactThresholdPercent < 0 || c.AutoCompactThresholdPercent > 100 {
		return errors.New("auto compact threshold must be between 0 and 100")
	}
	if c.Pruning.KeepLastNTurns < 0 || c.Pruning.SoftTrimThreshold < 0 || c.Pruning.SoftTrimHead < 0 || c.Pruning.SoftTrimTail < 0 || c.Pruning.HardClearAgeTurns < 0 {
		return errors.New("compaction pruning values must not be negative")
	}
	if c.WebFetch.ProxyConfigured {
		proxy, err := url.Parse(c.WebFetch.ProxyEndpoint)
		if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.Hostname() == "" {
			return errors.New("web fetch proxy_endpoint must be an absolute http(s) URL")
		}
	}
	if c.WebFetch.DomainsConfigured {
		for _, entry := range c.WebFetch.AllowedDomains {
			entry = strings.TrimRight(strings.TrimSpace(entry), "/.")
			host := strings.SplitN(entry, "/", 2)[0]
			if host == "" || strings.ContainsAny(host, "@:\\") || strings.ContainsAny(entry, "?#") || !strings.Contains(host, ".") {
				return fmt.Errorf("invalid web fetch allowed domain %q", entry)
			}
		}
	}
	return nil
}
