package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/semver"
)

const (
	defaultBaseURL  = "https://api.x.ai/v1"
	defaultProxyURL = "https://cli-chat-proxy.grok.com/v1"
)

type Config struct {
	APIKey                          string                     `json:"api_key,omitempty"`
	BaseURL                         string                     `json:"base_url,omitempty"`
	Model                           string                     `json:"model,omitempty"`
	Backend                         string                     `json:"backend,omitempty"`
	SystemPrompt                    string                     `json:"system_prompt,omitempty"`
	MaxSteps                        int                        `json:"max_steps,omitempty"`
	MCPServers                      map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	LSPServers                      map[string]LSPServerConfig `json:"lsp_servers,omitempty"`
	Permission                      PermissionConfig           `json:"permission,omitempty"`
	ContextWindow                   int                        `json:"context_window,omitempty"`
	AutoCompactThresholdPercent     int                        `json:"auto_compact_threshold_percent,omitempty"`
	Pruning                         PruningConfig              `json:"pruning"`
	Compat                          compat.Config              `json:"compat"`
	Skills                          SkillsConfig               `json:"skills,omitempty"`
	Plugins                         PluginsConfig              `json:"plugins,omitempty"`
	Marketplace                     MarketplaceConfig          `json:"marketplace,omitempty"`
	OfficialMarketplaceAutoRegister bool                       `json:"-"`
	HTTPTimeout                     time.Duration              `json:"-"`
	WebSearch                       WebSearchConfig            `json:"web_search,omitempty"`
	WebFetch                        WebFetchConfig             `json:"web_fetch,omitempty"`
	AskUserQuestion                 AskUserQuestionConfig      `json:"ask_user_question,omitempty"`
	AuthProviderCommand             string                     `json:"auth_provider_command,omitempty"`
	AuthTokenTTL                    time.Duration              `json:"-"`
	AuthPrincipalType               string                     `json:"auth_principal_type,omitempty"`
	AuthPrincipalID                 string                     `json:"auth_principal_id,omitempty"`
	ForceLoginTeams                 []string                   `json:"force_login_team_uuid,omitempty"`
	ForceLoginTeamConfigured        bool                       `json:"-"`
	DisableAPIKeyAuth               bool                       `json:"disable_api_key_auth,omitempty"`
	PreferredAuthMethod             string                     `json:"preferred_auth_method,omitempty"`
	ProxyBaseURL                    string                     `json:"proxy_base_url,omitempty"`
	ManagedConfigURL                string                     `json:"managed_config_url,omitempty"`
	DeploymentKey                   string                     `json:"deployment_key,omitempty"`
	FolderTrustEnabled              bool                       `json:"folder_trust_enabled"`
	AutoWakeEnabled                 bool                       `json:"-"`
	ModelProfiles                   map[string]ModelProfile    `json:"-"`
	compatConfigured                compat.Config
	autoWakeConfigured              bool
}

type ModelProfile struct {
	Model                       string
	BaseURL                     string
	APIKey                      string
	Backend                     string
	ContextWindow               int
	AutoCompactThresholdPercent *int
}

type WebSearchConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

type WebFetchConfig struct {
	Enabled           bool     `json:"enabled"`
	EnabledConfigured bool     `json:"-"`
	ProxyEndpoint     string   `json:"proxy_endpoint,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	ProxyConfigured   bool     `json:"-"`
	DomainsConfigured bool     `json:"-"`
}

type AskUserQuestionConfig struct {
	TimeoutEnabled bool   `json:"timeout_enabled"`
	TimeoutSeconds uint64 `json:"timeout_secs"`
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

type SkillsConfig struct {
	Paths    []string `json:"paths,omitempty" toml:"paths"`
	Ignore   []string `json:"ignore,omitempty" toml:"ignore"`
	Disabled []string `json:"disabled,omitempty" toml:"disabled"`
}

type PluginsConfig struct {
	Paths    []string `json:"paths,omitempty" toml:"paths"`
	Enabled  []string `json:"enabled,omitempty" toml:"enabled"`
	Disabled []string `json:"disabled,omitempty" toml:"disabled"`
}

type MarketplaceConfig struct {
	Sources                          []MarketplaceSourceConfig `json:"sources,omitempty" toml:"sources"`
	OfficialMarketplaceAutoInstalled bool                      `json:"official_marketplace_auto_installed,omitempty" toml:"official_marketplace_auto_installed"`
}

type MarketplaceSourceConfig struct {
	Name   string `json:"name" toml:"name"`
	Path   string `json:"path,omitempty" toml:"path"`
	Git    string `json:"git,omitempty" toml:"git"`
	Branch string `json:"branch,omitempty" toml:"branch"`
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
	Command           string            `json:"command" toml:"command"`
	Args              []string          `json:"args,omitempty" toml:"args"`
	Env               map[string]string `json:"env,omitempty" toml:"env"`
	URL               string            `json:"url,omitempty" toml:"url"`
	Type              string            `json:"type,omitempty" toml:"type"`
	Headers           map[string]string `json:"headers,omitempty" toml:"headers"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty" toml:"bearer_token_env_var"`
	Enabled           *bool             `json:"enabled,omitempty" toml:"enabled"`
}

type LSPServerConfig struct {
	Command               string            `json:"command" toml:"command"`
	Transport             string            `json:"transport,omitempty" toml:"transport"`
	WorkspaceFolder       string            `json:"workspace_folder,omitempty" toml:"workspace_folder"`
	StartupTimeoutMS      int               `json:"startup_timeout,omitempty" toml:"startup_timeout"`
	ShutdownTimeoutMS     int               `json:"shutdown_timeout,omitempty" toml:"shutdown_timeout"`
	RestartOnCrash        bool              `json:"restart_on_crash,omitempty" toml:"restart_on_crash"`
	MaxRestarts           *int              `json:"max_restarts,omitempty" toml:"max_restarts"`
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
	Skills        SkillsConfig               `json:"skills,omitempty" toml:"skills"`
	Plugins       PluginsConfig              `json:"plugins,omitempty" toml:"plugins"`
	Marketplace   MarketplaceConfig          `json:"marketplace,omitempty" toml:"marketplace"`
	Features      struct {
		WebFetch *bool `json:"web_fetch,omitempty" toml:"web_fetch"`
		AutoWake *bool `json:"auto_wake,omitempty" toml:"auto_wake"`
	} `json:"features,omitempty" toml:"features"`
	AuthProviderCommand string            `json:"auth_provider_command,omitempty" toml:"auth_provider_command"`
	AuthTokenTTL        int64             `json:"auth_token_ttl,omitempty" toml:"auth_token_ttl"`
	GrokComConfig       fileGrokComConfig `json:"grok_com_config,omitempty" toml:"grok_com_config"`
	Auth                fileAuthConfig    `json:"auth,omitempty" toml:"auth"`
	Models              struct {
		Default   string `toml:"default"`
		WebSearch string `toml:"web_search"`
	} `json:"-" toml:"models"`
	ModelEntries map[string]modelConfig `json:"-" toml:"model"`
	Toolset      struct {
		WebFetch        fileWebFetchConfig        `json:"web_fetch,omitempty" toml:"web_fetch"`
		AskUserQuestion fileAskUserQuestionConfig `json:"ask_user_question,omitempty" toml:"ask_user_question"`
	} `json:"toolset,omitempty" toml:"toolset"`
	Endpoints   fileEndpointsConfig   `json:"endpoints,omitempty" toml:"endpoints"`
	FolderTrust fileFolderTrustConfig `json:"folder_trust,omitempty" toml:"folder_trust"`
}

type fileFolderTrustConfig struct {
	Enabled *bool `json:"enabled,omitempty" toml:"enabled"`
}

type fileEndpointsConfig struct {
	CLIChatProxyBaseURL string `json:"cli_chat_proxy_base_url,omitempty" toml:"cli_chat_proxy_base_url"`
	ManagedConfigURL    string `json:"managed_config_url,omitempty" toml:"managed_config_url"`
	DeploymentKey       string `json:"deployment_key,omitempty" toml:"deployment_key"`
}

type fileAuthConfig struct {
	PreferredMethod string `json:"preferred_method,omitempty" toml:"preferred_method"`
}

type requirementsFile struct {
	Permission    *PermissionConfig       `toml:"permission"`
	GrokComConfig *requirementsGrokConfig `toml:"grok_com_config"`
	Auth          *requirementsAuthConfig `toml:"auth"`
	Toolset       struct {
		AskUserQuestion *fileAskUserQuestionConfig `toml:"ask_user_question"`
	} `toml:"toolset"`
}

type requirementsAuthConfig struct {
	PreferredMethod *string `toml:"preferred_method"`
}

type requirementsGrokConfig struct {
	AuthProviderCommand *string `toml:"auth_provider_command"`
	AuthTokenTTL        *int64  `toml:"auth_token_ttl"`
	ForceLoginTeamUUID  any     `toml:"force_login_team_uuid"`
	DisableAPIKeyAuth   *bool   `toml:"disable_api_key_auth"`
	OAuth2              *struct {
		PrincipalType *string `toml:"principal_type"`
		PrincipalID   *string `toml:"principal_id"`
	} `toml:"oauth2"`
}

type fileGrokComConfig struct {
	AuthProviderCommand string `json:"auth_provider_command,omitempty" toml:"auth_provider_command"`
	AuthTokenTTL        int64  `json:"auth_token_ttl,omitempty" toml:"auth_token_ttl"`
	ForceLoginTeamUUID  any    `json:"force_login_team_uuid,omitempty" toml:"force_login_team_uuid"`
	DisableAPIKeyAuth   *bool  `json:"disable_api_key_auth,omitempty" toml:"disable_api_key_auth"`
	OAuth2              struct {
		PrincipalType string `json:"principal_type,omitempty" toml:"principal_type"`
		PrincipalID   string `json:"principal_id,omitempty" toml:"principal_id"`
	} `json:"oauth2,omitempty" toml:"oauth2"`
}

type fileWebFetchConfig struct {
	ProxyEndpoint  *string  `json:"proxy_endpoint,omitempty" toml:"proxy_endpoint"`
	AllowedDomains []string `json:"allowed_domains,omitempty" toml:"allowed_domains"`
}

type fileAskUserQuestionConfig struct {
	TimeoutEnabled *bool   `json:"timeout_enabled,omitempty" toml:"timeout_enabled"`
	TimeoutSeconds *uint64 `json:"timeout_secs,omitempty" toml:"timeout_secs"`
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
	Mcps   *bool `json:"mcps,omitempty" toml:"mcps"`
	Hooks  *bool `json:"hooks,omitempty" toml:"hooks"`
}

type fileCompatConfig struct {
	Cursor fileVendorCompat `json:"cursor,omitempty" toml:"cursor"`
	Claude fileVendorCompat `json:"claude,omitempty" toml:"claude"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		BaseURL:                     defaultBaseURL,
		ProxyBaseURL:                defaultProxyURL,
		Backend:                     "responses",
		MaxSteps:                    20,
		HTTPTimeout:                 10 * time.Minute,
		ContextWindow:               131072,
		AutoCompactThresholdPercent: 85,
		Compat:                      compat.Default(),
		FolderTrustEnabled:          true,
		AutoWakeEnabled:             true,
		AskUserQuestion:             AskUserQuestionConfig{TimeoutEnabled: true, TimeoutSeconds: 30 * 60},
		Pruning:                     PruningConfig{Enabled: true, KeepLastNTurns: 3, SoftTrimThreshold: 4000, SoftTrimHead: 1500, SoftTrimTail: 1500, HardClearAgeTurns: 10},
	}
	if path == "" {
		var err error
		path, err = discoverDefaultPath()
		if err != nil {
			return Config{}, err
		}
	}

	managedPaths := managedConfigPaths()
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if disk, ok, err := loadMergedTOML(managedPaths); err != nil {
			return Config{}, err
		} else if ok {
			if err := applyFileConfig(&cfg, &disk); err != nil {
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
			if err := applyFileConfig(&cfg, &disk); err != nil {
				return Config{}, err
			}
		}
	} else {
		layers := append(managedPaths, path)
		disk, ok, err := loadMergedTOML(layers)
		if err != nil {
			return Config{}, err
		}
		if ok {
			if err := applyFileConfig(&cfg, &disk); err != nil {
				return Config{}, err
			}
		}
	}

	applyEnv(&cfg)
	if err := applyRequirementsFiles(&cfg, requirementsPaths()); err != nil {
		return Config{}, err
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.WebSearch.BaseURL = strings.TrimRight(cfg.WebSearch.BaseURL, "/")
	return cfg, nil
}

func applyFileConfig(cfg *Config, disk *fileConfig) error {
	mergeModelProfiles(cfg, disk.ModelEntries)
	applyModelConfig(disk)
	if disk.Models.WebSearch != "" {
		entry, ok := disk.ModelEntries[disk.Models.WebSearch]
		if !ok {
			return fmt.Errorf("web search model %q is not defined", disk.Models.WebSearch)
		}
		if entry.Backend != "" && entry.Backend != "responses" {
			return errors.New("web search model backend must be responses")
		}
		cfg.WebSearch = WebSearchConfig{Enabled: true, APIKey: entry.APIKey, BaseURL: entry.BaseURL, Model: entry.Model}
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
	if disk.MCPServers != nil {
		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]MCPServerConfig)
		}
		for name, server := range disk.MCPServers {
			cfg.MCPServers[name] = server
		}
	}
	if disk.LSPServers != nil {
		if cfg.LSPServers == nil {
			cfg.LSPServers = make(map[string]LSPServerConfig)
		}
		for name, server := range disk.LSPServers {
			cfg.LSPServers[name] = server
		}
	}
	if disk.Permission.Rules != nil {
		cfg.Permission = disk.Permission
	}
	if disk.Toolset.WebFetch.ProxyEndpoint != nil {
		cfg.WebFetch.ProxyEndpoint = *disk.Toolset.WebFetch.ProxyEndpoint
		cfg.WebFetch.ProxyConfigured = true
	}
	if disk.Features.WebFetch != nil {
		cfg.WebFetch.Enabled, cfg.WebFetch.EnabledConfigured = *disk.Features.WebFetch, true
	}
	if disk.Features.AutoWake != nil {
		cfg.AutoWakeEnabled, cfg.autoWakeConfigured = *disk.Features.AutoWake, true
	}
	if disk.Toolset.WebFetch.AllowedDomains != nil {
		cfg.WebFetch.AllowedDomains = append([]string(nil), disk.Toolset.WebFetch.AllowedDomains...)
		cfg.WebFetch.DomainsConfigured = true
	}
	applyAskUserQuestionConfig(&cfg.AskUserQuestion, disk.Toolset.AskUserQuestion)
	if disk.ContextWindow > 0 {
		cfg.ContextWindow = disk.ContextWindow
	}
	if disk.Session.AutoCompactThresholdPercent != nil {
		cfg.AutoCompactThresholdPercent = *disk.Session.AutoCompactThresholdPercent
	}
	applyPruningConfig(&cfg.Pruning, disk.Compaction.Pruning)
	applyCompatConfig(&cfg.Compat, disk.Compat)
	markCompatConfig(&cfg.compatConfigured, disk.Compat)
	if disk.Skills.Paths != nil {
		cfg.Skills.Paths = append([]string(nil), disk.Skills.Paths...)
	}
	if disk.Skills.Ignore != nil {
		cfg.Skills.Ignore = append([]string(nil), disk.Skills.Ignore...)
	}
	if disk.Skills.Disabled != nil {
		cfg.Skills.Disabled = append([]string(nil), disk.Skills.Disabled...)
	}
	if disk.Plugins.Paths != nil {
		cfg.Plugins.Paths = append([]string(nil), disk.Plugins.Paths...)
	}
	if disk.Plugins.Enabled != nil {
		cfg.Plugins.Enabled = append([]string(nil), disk.Plugins.Enabled...)
	}
	if disk.Plugins.Disabled != nil {
		cfg.Plugins.Disabled = append([]string(nil), disk.Plugins.Disabled...)
	}
	if disk.Marketplace.Sources != nil {
		cfg.Marketplace.Sources = append([]MarketplaceSourceConfig(nil), disk.Marketplace.Sources...)
	}
	if disk.Marketplace.OfficialMarketplaceAutoInstalled {
		cfg.Marketplace.OfficialMarketplaceAutoInstalled = true
	}
	if disk.FolderTrust.Enabled != nil {
		cfg.FolderTrustEnabled = *disk.FolderTrust.Enabled
	}
	if disk.Endpoints.CLIChatProxyBaseURL != "" {
		cfg.ProxyBaseURL = strings.TrimRight(disk.Endpoints.CLIChatProxyBaseURL, "/")
	}
	if disk.Endpoints.ManagedConfigURL != "" {
		cfg.ManagedConfigURL = disk.Endpoints.ManagedConfigURL
	}
	if disk.Endpoints.DeploymentKey != "" {
		cfg.DeploymentKey = disk.Endpoints.DeploymentKey
	}
	if disk.GrokComConfig.AuthProviderCommand != "" || disk.AuthProviderCommand != "" {
		cfg.AuthProviderCommand = disk.GrokComConfig.AuthProviderCommand
		if cfg.AuthProviderCommand == "" {
			cfg.AuthProviderCommand = disk.AuthProviderCommand
		}
	}
	authTokenTTL := disk.GrokComConfig.AuthTokenTTL
	if authTokenTTL == 0 {
		authTokenTTL = disk.AuthTokenTTL
	}
	if authTokenTTL > 0 {
		cfg.AuthTokenTTL = time.Duration(authTokenTTL) * time.Second
	}
	if disk.GrokComConfig.OAuth2.PrincipalType != "" {
		cfg.AuthPrincipalType = strings.TrimSpace(disk.GrokComConfig.OAuth2.PrincipalType)
	}
	if disk.GrokComConfig.OAuth2.PrincipalID != "" {
		cfg.AuthPrincipalID = strings.TrimSpace(disk.GrokComConfig.OAuth2.PrincipalID)
	}
	if disk.GrokComConfig.ForceLoginTeamUUID != nil {
		teams, configured, err := forceLoginTeams(disk.GrokComConfig.ForceLoginTeamUUID)
		if err != nil {
			return err
		}
		cfg.ForceLoginTeams, cfg.ForceLoginTeamConfigured = teams, configured
	}
	if disk.GrokComConfig.DisableAPIKeyAuth != nil {
		cfg.DisableAPIKeyAuth = *disk.GrokComConfig.DisableAPIKeyAuth
	}
	if disk.Auth.PreferredMethod != "" {
		cfg.PreferredAuthMethod = strings.ToLower(strings.TrimSpace(disk.Auth.PreferredMethod))
	}
	if disk.HTTPTimeout != "" {
		duration, err := time.ParseDuration(disk.HTTPTimeout)
		if err != nil {
			return fmt.Errorf("parse http_timeout: %w", err)
		}
		cfg.HTTPTimeout = duration
	}
	return nil
}

func mergeModelProfiles(cfg *Config, entries map[string]modelConfig) {
	if len(entries) == 0 {
		return
	}
	if cfg.ModelProfiles == nil {
		cfg.ModelProfiles = make(map[string]ModelProfile)
	}
	for name, entry := range entries {
		profile := cfg.ModelProfiles[name]
		if entry.Model != "" {
			profile.Model = entry.Model
		}
		if entry.BaseURL != "" {
			profile.BaseURL = entry.BaseURL
		}
		if entry.APIKey != "" {
			profile.APIKey = entry.APIKey
		} else if key := firstConfiguredEnv(entry.EnvKey); key != "" {
			profile.APIKey = key
		}
		if entry.Backend != "" {
			profile.Backend = entry.Backend
		}
		if entry.ContextWindow > 0 {
			profile.ContextWindow = entry.ContextWindow
		}
		if entry.AutoCompactThresholdPercent != nil {
			value := *entry.AutoCompactThresholdPercent
			profile.AutoCompactThresholdPercent = &value
		}
		cfg.ModelProfiles[name] = profile
	}
}

func (c Config) ResolveModel(slug string) (Config, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Config{}, false
	}
	name, profile, ok := c.modelProfile(slug)
	if !ok {
		if slug == c.Model {
			return c, true
		}
		return Config{}, false
	}
	result := c
	result.Model = profile.Model
	if result.Model == "" {
		result.Model = name
	}
	if profile.BaseURL != "" {
		result.BaseURL = strings.TrimRight(profile.BaseURL, "/")
	}
	if profile.APIKey != "" {
		result.APIKey = profile.APIKey
	}
	if profile.Backend != "" {
		result.Backend = profile.Backend
	}
	if profile.ContextWindow > 0 {
		result.ContextWindow = profile.ContextWindow
	}
	if profile.AutoCompactThresholdPercent != nil {
		result.AutoCompactThresholdPercent = *profile.AutoCompactThresholdPercent
	}
	return result, true
}

func (c Config) ModelSlugs() []string {
	names := make([]string, 0, len(c.ModelProfiles))
	for name := range c.ModelProfiles {
		names = append(names, name)
	}
	if len(names) == 0 && c.Model != "" {
		names = append(names, c.Model)
	}
	sort.Strings(names)
	return names
}

func (c Config) modelProfile(slug string) (string, ModelProfile, bool) {
	if profile, ok := c.ModelProfiles[slug]; ok {
		return slug, profile, true
	}
	names := make([]string, 0, len(c.ModelProfiles))
	for name := range c.ModelProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := c.ModelProfiles[name]
		if profile.Model == slug {
			return name, profile, true
		}
	}
	return "", ModelProfile{}, false
}

func applyCompatConfig(target *compat.Config, source fileCompatConfig) {
	applyVendorCompat(&target.Cursor, source.Cursor)
	applyVendorCompat(&target.Claude, source.Claude)
}

func markCompatConfig(target *compat.Config, source fileCompatConfig) {
	markVendorCompat(&target.Cursor, source.Cursor)
	markVendorCompat(&target.Claude, source.Claude)
}

func markVendorCompat(target *compat.Vendor, source fileVendorCompat) {
	target.Skills = target.Skills || source.Skills != nil
	target.Rules = target.Rules || source.Rules != nil
	target.Agents = target.Agents || source.Agents != nil
	target.Mcps = target.Mcps || source.Mcps != nil
	target.Hooks = target.Hooks || source.Hooks != nil
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
	if source.Mcps != nil {
		target.Mcps = *source.Mcps
	}
	if source.Hooks != nil {
		target.Hooks = *source.Hooks
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

func applyAskUserQuestionConfig(target *AskUserQuestionConfig, source fileAskUserQuestionConfig) {
	if source.TimeoutEnabled != nil {
		target.TimeoutEnabled = *source.TimeoutEnabled
	}
	if source.TimeoutSeconds != nil {
		target.TimeoutSeconds = normalizedQuestionTimeout(*source.TimeoutSeconds)
	}
}

func normalizedQuestionTimeout(seconds uint64) uint64 {
	if seconds == 0 || seconds > uint64((1<<63-1)/int64(time.Second)) {
		return 30 * 60
	}
	return seconds
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
	if value := strings.TrimSpace(os.Getenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS")); value != "" {
		if seconds, err := strconv.ParseUint(value, 10, 64); err == nil && seconds > 0 && seconds <= uint64((1<<63-1)/int64(time.Second)) {
			cfg.AskUserQuestion.TimeoutSeconds = seconds
		}
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
	if value := strings.TrimSpace(os.Getenv("GROK_AUTH_PROVIDER_COMMAND")); value != "" {
		cfg.AuthProviderCommand = value
	}
	if value := strings.TrimSpace(os.Getenv("GROK_OAUTH2_PRINCIPAL_TYPE")); value != "" {
		cfg.AuthPrincipalType = value
	}
	if value := strings.TrimSpace(os.Getenv("GROK_OAUTH2_PRINCIPAL_ID")); value != "" {
		cfg.AuthPrincipalID = value
	}
	if value := strings.TrimSpace(os.Getenv("GROK_AUTH_TOKEN_TTL")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			cfg.AuthTokenTTL = time.Duration(seconds) * time.Second
		}
	}
	if value := strings.TrimSpace(os.Getenv("GROK_DISABLE_API_KEY_AUTH")); value != "" {
		if enabled, ok := envBoolValue(value); ok && enabled {
			cfg.DisableAPIKeyAuth = true
		}
	}
	if value := strings.TrimSpace(os.Getenv("GROK_CLI_CHAT_PROXY_BASE_URL")); value != "" {
		cfg.ProxyBaseURL = strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(os.Getenv("GROK_MANAGED_CONFIG_URL")); value != "" {
		cfg.ManagedConfigURL = value
	}
	if value := strings.TrimSpace(os.Getenv("GROK_DEPLOYMENT_KEY")); value != "" {
		cfg.DeploymentKey = value
	}
	applyCompatEnv(&cfg.Compat.Cursor, "CURSOR")
	applyCompatEnv(&cfg.Compat.Claude, "CLAUDE")
	if value, ok := envBool("GROK_FOLDER_TRUST"); ok {
		cfg.FolderTrustEnabled = value
	}
	if value, ok := envBool("GROK_WEB_FETCH"); ok {
		cfg.WebFetch.Enabled, cfg.WebFetch.EnabledConfigured = value, true
	}
	if value, ok := envBool("GROK_AUTO_WAKE"); ok {
		cfg.AutoWakeEnabled, cfg.autoWakeConfigured = value, true
	}
	if value, ok := envBool("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER"); ok {
		cfg.OfficialMarketplaceAutoRegister = value
	}
}

func (c Config) ManagedPolicyURL() string {
	if strings.TrimSpace(c.ManagedConfigURL) != "" {
		return strings.TrimSpace(c.ManagedConfigURL)
	}
	return strings.TrimRight(c.ProxyBaseURL, "/") + "/deployment/config"
}

func requirementsPaths() []string {
	paths := make([]string, 0, 2)
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		paths = append(paths, filepath.Join(home, "requirements.toml"))
	} else if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".grok", "requirements.toml"))
	}
	if runtime.GOOS != "windows" {
		paths = append(paths, "/etc/grok/requirements.toml")
	}
	return paths
}

func managedConfigPaths() []string {
	paths := make([]string, 0, 2)
	if runtime.GOOS != "windows" {
		paths = append(paths, "/etc/grok/managed_config.toml")
	}
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		paths = append(paths, filepath.Join(home, "managed_config.toml"))
	} else if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".grok", "managed_config.toml"))
	}
	return paths
}

func loadMergedTOML(paths []string) (fileConfig, bool, error) {
	merged := make(map[string]any)
	found := false
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fileConfig{}, false, fmt.Errorf("read config %q: %w", path, err)
		}
		var layer map[string]any
		if err := toml.Unmarshal(data, &layer); err != nil {
			return fileConfig{}, false, fmt.Errorf("parse config %q: %w", path, err)
		}
		if err := applyVersionOverrides(layer, version.Current); err != nil {
			return fileConfig{}, false, fmt.Errorf("parse config %q: %w", path, err)
		}
		deepMergeMap(merged, layer)
		found = true
	}
	if !found {
		return fileConfig{}, false, nil
	}
	data, err := toml.Marshal(merged)
	if err != nil {
		return fileConfig{}, false, err
	}
	var disk fileConfig
	if err := toml.Unmarshal(data, &disk); err != nil {
		return fileConfig{}, false, err
	}
	return disk, true, nil
}

func deepMergeMap(base, override map[string]any) {
	for key, value := range override {
		if overrideTable, ok := value.(map[string]any); ok {
			if baseTable, ok := base[key].(map[string]any); ok {
				deepMergeMap(baseTable, overrideTable)
				continue
			}
		}
		base[key] = value
	}
}

type versionPatch struct {
	minimum string
	maximum string
	patch   map[string]any
}

func applyVersionOverrides(layer map[string]any, current string) error {
	raw, ok := layer["version_overrides"]
	delete(layer, "version_overrides")
	if !ok {
		return nil
	}
	current, err := normalizedSemver(current)
	if err != nil {
		return fmt.Errorf("current version: %w", err)
	}
	entries, err := versionEntries(raw)
	if err != nil {
		return err
	}
	patches := make([]versionPatch, 0, len(entries))
	for index, entry := range entries {
		patch := make(map[string]any, len(entry))
		for key, value := range entry {
			patch[key] = value
		}
		minimum, err := patchVersion(patch, "minimum_version", "v0.0.0")
		if err != nil {
			return fmt.Errorf("version_overrides[%d].minimum_version: %w", index, err)
		}
		maximum, err := patchVersion(patch, "maximum_version", "")
		if err != nil {
			return fmt.Errorf("version_overrides[%d].maximum_version: %w", index, err)
		}
		delete(patch, "version_overrides")
		delete(patch, "campaigns")
		patches = append(patches, versionPatch{minimum: minimum, maximum: maximum, patch: patch})
	}
	sort.SliceStable(patches, func(i, j int) bool { return semver.Compare(patches[i].minimum, patches[j].minimum) < 0 })
	for _, candidate := range patches {
		if semver.Compare(current, candidate.minimum) < 0 || candidate.maximum != "" && semver.Compare(current, candidate.maximum) > 0 {
			continue
		}
		deepMergeMap(layer, candidate.patch)
	}
	return nil
}

func versionEntries(raw any) ([]map[string]any, error) {
	if entries, ok := raw.([]map[string]any); ok {
		return entries, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, errors.New("version_overrides must be an array of tables")
	}
	entries := make([]map[string]any, 0, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("version_overrides must be an array of tables")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func patchVersion(patch map[string]any, key, fallback string) (string, error) {
	raw, ok := patch[key]
	delete(patch, key)
	if !ok {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.New("must be a semver string")
	}
	if strings.HasPrefix(strings.TrimSpace(value), "v") {
		return "", fmt.Errorf("%q is not valid semver", value)
	}
	return normalizedSemver(value)
}

func normalizedSemver(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return "", fmt.Errorf("%q is not valid semver", strings.TrimPrefix(value, "v"))
	}
	return value, nil
}

func applyRequirementsFiles(cfg *Config, paths []string) error {
	envFailClosed, _ := envBoolValue(os.Getenv("GROK_MANAGED_CONFIG_FAIL_CLOSED"))
	envDisablesAPIKey, _ := envBoolValue(os.Getenv("GROK_DISABLE_API_KEY_AUTH"))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if envFailClosed {
				return fmt.Errorf("read requirements %q: %w", path, err)
			}
			continue
		}
		if err := applyRequirementsData(cfg, data, path, envFailClosed, envDisablesAPIKey); err != nil {
			return err
		}
	}
	if data := readManagedRequirements(); data != nil {
		if err := applyRequirementsData(cfg, data, mdmRequirementsSource, envFailClosed, envDisablesAPIKey); err != nil {
			return err
		}
	}
	return nil
}

func applyRequirementsData(cfg *Config, data []byte, source string, envFailClosed, envDisablesAPIKey bool) error {
	fileFailClosed := false
	var raw map[string]any
	if toml.Unmarshal(data, &raw) == nil {
		fileFailClosed, _ = raw["fail_closed"].(bool)
		if err := applyVersionOverrides(raw, version.Current); err != nil {
			if envFailClosed || fileFailClosed {
				return fmt.Errorf("parse requirements %q: %w", source, err)
			}
			return nil
		}
		var err error
		data, err = toml.Marshal(raw)
		if err != nil {
			return err
		}
	}
	var requirement requirementsFile
	if err := toml.Unmarshal(data, &requirement); err != nil {
		if envFailClosed || fileFailClosed {
			return fmt.Errorf("parse requirements %q: %w", source, err)
		}
		return nil
	}
	if requirement.Permission != nil {
		cfg.Permission = *requirement.Permission
	}
	if requirement.Auth != nil && requirement.Auth.PreferredMethod != nil {
		cfg.PreferredAuthMethod = strings.ToLower(strings.TrimSpace(*requirement.Auth.PreferredMethod))
	}
	if requirement.Toolset.AskUserQuestion != nil {
		applyAskUserQuestionConfig(&cfg.AskUserQuestion, *requirement.Toolset.AskUserQuestion)
	}
	if requirement.GrokComConfig == nil {
		return nil
	}
	managed := requirement.GrokComConfig
	if managed.AuthProviderCommand != nil {
		cfg.AuthProviderCommand = *managed.AuthProviderCommand
	}
	if managed.AuthTokenTTL != nil {
		if *managed.AuthTokenTTL < 0 {
			return fmt.Errorf("parse requirements %q: auth_token_ttl must not be negative", source)
		}
		cfg.AuthTokenTTL = time.Duration(*managed.AuthTokenTTL) * time.Second
	}
	if managed.OAuth2 != nil {
		if managed.OAuth2.PrincipalType != nil {
			cfg.AuthPrincipalType = strings.TrimSpace(*managed.OAuth2.PrincipalType)
		}
		if managed.OAuth2.PrincipalID != nil {
			cfg.AuthPrincipalID = strings.TrimSpace(*managed.OAuth2.PrincipalID)
		}
	}
	if managed.ForceLoginTeamUUID != nil {
		teams, configured, err := forceLoginTeams(managed.ForceLoginTeamUUID)
		if err != nil {
			return fmt.Errorf("parse requirements %q: %w", source, err)
		}
		cfg.ForceLoginTeams, cfg.ForceLoginTeamConfigured = teams, configured
	}
	if managed.DisableAPIKeyAuth != nil && !envDisablesAPIKey {
		cfg.DisableAPIKeyAuth = *managed.DisableAPIKeyAuth
	}
	return nil
}

func forceLoginTeams(value any) ([]string, bool, error) {
	switch typed := value.(type) {
	case nil:
		return nil, false, nil
	case string:
		return []string{typed}, true, nil
	case []string:
		return append([]string(nil), typed...), true, nil
	case []any:
		teams := make([]string, 0, len(typed))
		for _, item := range typed {
			team, ok := item.(string)
			if !ok {
				return nil, false, errors.New("force_login_team_uuid must be a string or array of strings")
			}
			teams = append(teams, team)
		}
		return teams, true, nil
	default:
		return nil, false, errors.New("force_login_team_uuid must be a string or array of strings")
	}
}

func envBoolValue(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled", "":
		return false, true
	default:
		return false, false
	}
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
		"MCPS":   &target.Mcps,
		"HOOKS":  &target.Hooks,
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
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, "config.toml"), nil
	}
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
	if err := c.ValidateAuthPolicy(); err != nil {
		return err
	}
	if c.APIKey == "" {
		return errors.New("missing credentials: set GORK_API_KEY or XAI_API_KEY, or run gork login")
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

func (c Config) ValidateAuthPolicy() error {
	if c.PreferredAuthMethod != "" && c.PreferredAuthMethod != "api_key" && c.PreferredAuthMethod != "oidc" {
		return errors.New("auth preferred_method must be api_key or oidc")
	}
	if c.PreferredAuthMethod == "api_key" && (c.DisableAPIKeyAuth || c.ForceLoginTeamConfigured) {
		return errors.New("auth preferred_method api_key conflicts with API-key authentication policy")
	}
	return nil
}
