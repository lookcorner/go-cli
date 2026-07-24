package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/theme"
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
	DefaultModelID                  string                     `json:"-"`
	Backend                         string                     `json:"backend,omitempty"`
	SystemPrompt                    string                     `json:"system_prompt,omitempty"`
	MaxSteps                        int                        `json:"max_steps,omitempty"`
	Env                             map[string]string          `json:"env,omitempty"`
	MCPServers                      map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	DisabledMCPServers              []string                   `json:"disabled_mcp_servers,omitempty"`
	DisabledMCPTools                map[string][]string        `json:"disabled_mcp_tools,omitempty"`
	LSPServers                      map[string]LSPServerConfig `json:"lsp_servers,omitempty"`
	Permission                      PermissionConfig           `json:"permission,omitempty"`
	AutoMode                        AutoModeConfig             `json:"auto_mode,omitempty"`
	ContextWindow                   int                        `json:"context_window,omitempty"`
	AutoCompactThresholdPercent     int                        `json:"auto_compact_threshold_percent,omitempty"`
	TwoPassCompaction               bool                       `json:"two_pass_compaction,omitempty"`
	Pruning                         PruningConfig              `json:"pruning"`
	Memory                          memory.Config              `json:"memory"`
	Compat                          compat.Config              `json:"compat"`
	Skills                          SkillsConfig               `json:"skills,omitempty"`
	Plugins                         PluginsConfig              `json:"plugins,omitempty"`
	Marketplace                     MarketplaceConfig          `json:"marketplace,omitempty"`
	OfficialMarketplaceAutoRegister bool                       `json:"-"`
	SubscriptionTier                *string                    `json:"-"`
	SubscriptionTierDisplay         *string                    `json:"-"`
	OnDemandEnabled                 *bool                      `json:"-"`
	SharingEnabled                  bool                       `json:"-"`
	Announcements                   []RemoteAnnouncement       `json:"-"`
	AllowAccess                     *bool                      `json:"-"`
	GateMessage                     *string                    `json:"-"`
	GateURL                         *string                    `json:"-"`
	GateLabel                       *string                    `json:"-"`
	ShowResolvedModel               *bool                      `json:"-"`
	HTTPTimeout                     time.Duration              `json:"-"`
	WebSearch                       WebSearchConfig            `json:"web_search,omitempty"`
	WebFetch                        WebFetchConfig             `json:"web_fetch,omitempty"`
	AskUserQuestion                 AskUserQuestionConfig      `json:"ask_user_question,omitempty"`
	Toolset                         ToolsetConfig              `json:"toolset"`
	Goal                            GoalConfig                 `json:"goal"`
	UI                              UIConfig                   `json:"ui"`
	Dashboard                       DashboardConfig            `json:"dashboard"`
	AuthProviderCommand             string                     `json:"auth_provider_command,omitempty"`
	AuthTokenTTL                    time.Duration              `json:"-"`
	AuthPrincipalType               string                     `json:"auth_principal_type,omitempty"`
	AuthPrincipalID                 string                     `json:"auth_principal_id,omitempty"`
	ForceLoginTeams                 []string                   `json:"force_login_team_uuid,omitempty"`
	ForceLoginTeamConfigured        bool                       `json:"-"`
	DisableAPIKeyAuth               bool                       `json:"disable_api_key_auth,omitempty"`
	DisableBypassPermissionsMode    bool                       `json:"-"`
	PreferredAuthMethod             string                     `json:"preferred_auth_method,omitempty"`
	ProxyBaseURL                    string                     `json:"proxy_base_url,omitempty"`
	ManagedConfigURL                string                     `json:"managed_config_url,omitempty"`
	DeploymentKey                   string                     `json:"deployment_key,omitempty"`
	FolderTrustEnabled              bool                       `json:"folder_trust_enabled"`
	AutoWakeEnabled                 bool                       `json:"-"`
	FeedbackEnabled                 bool                       `json:"-"`
	ModelProfiles                   map[string]ModelProfile    `json:"-"`
	AllowedModels                   []string                   `json:"-"`
	HiddenModels                    []string                   `json:"-"`
	DisabledModels                  []string                   `json:"-"`
	ReasoningEffort                 string                     `json:"-"`
	ModelSupportsReasoningEffort    bool                       `json:"-"`
	ModelReasoningEfforts           []ReasoningEffortOption    `json:"-"`
	compatConfigured                compat.Config
	autoWakeConfigured              bool
	feedbackConfigured              bool
	feedbackEnvConfigured           bool
	twoPassCompactionConfigured     bool
	memoryConfigured                bool
	memoryInjectionConfigured       bool
	memoryFlushConfigured           bool
	memorySearchConfigured          bool
	memoryDreamConfigured           bool
	goalVerifierConfigured          bool
	goalClassifierMaxConfigured     bool
	goalPlannerConfigured           bool
	goalPlannerResolved             bool
	goalSummaryConfigured           bool
	goalSummaryResolved             bool
	goalStrategistEveryConfigured   bool
	autoModeEnabledConfigured       bool
	autoModePromptConfigured        bool
	autoModeModelConfigured         bool
	autoModeReasoningConfigured     bool
	uiTextSelectionConfigured       bool
	uiTextSelectionInvalid          bool
	uiSelectionHighlightDuration    *uint64
	uiDoubleClickAction             *string
	uiPermissionModeConfigured      bool
	modelConfigured                 bool
	defaultModelConfigured          bool
	allowedModelsConfigured         bool
	hiddenModelsConfigured          bool
	disabledModelsConfigured        bool
}

type AutoModeConfig struct {
	Enabled         *bool  `json:"enabled,omitempty" toml:"enabled"`
	PromptType      string `json:"prompt_type,omitempty" toml:"prompt_type"`
	ClassifierModel string `json:"classifier_model,omitempty" toml:"classifier_model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" toml:"reasoning_effort"`
}

type ToolsetConfig struct {
	FileToolset string         `json:"file_toolset"`
	Hashline    HashlineConfig `json:"hashline"`
}

type HashlineConfig struct {
	Scheme    string `json:"scheme"`
	HashLen   int    `json:"hash_len"`
	ChunkSize int    `json:"chunk_size"`
}

func (c Config) AutoModeEnabled() bool {
	return c.AutoMode.Enabled == nil || *c.AutoMode.Enabled
}

func (c Config) AutoModePromptType() string {
	if c.AutoMode.PromptType == "" {
		return "full"
	}
	return c.AutoMode.PromptType
}

type ModelProfile struct {
	Model                       string
	Name                        string
	Description                 string
	Hidden                      bool
	BaseURL                     string
	APIKey                      string
	Backend                     string
	ContextWindow               int
	AutoCompactThresholdPercent *int
	ReasoningEffort             string
	SupportsReasoningEffort     bool
	ReasoningEfforts            []ReasoningEffortOption
	hiddenConfigured            bool
	supportsReasoningConfigured bool
}

type ReasoningEffortOption struct {
	ID          string `json:"id" toml:"id"`
	Value       string `json:"value" toml:"value"`
	Label       string `json:"label" toml:"label"`
	Description string `json:"description,omitempty" toml:"description"`
	Default     bool   `json:"default" toml:"default"`
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

type UIConfig struct {
	Theme                string  `json:"theme"`
	KeepTextSelection    string  `json:"keep_text_selection"`
	WordSeparators       *string `json:"word_separators,omitempty"`
	MouseReportingToggle bool    `json:"mouse_reporting_toggle,omitempty"`
	VimMode              bool    `json:"vim_mode,omitempty"`
	CompactMode          bool    `json:"compact_mode,omitempty"`
	ShowTimestamps       bool    `json:"show_timestamps"`
	ShowTimeline         bool    `json:"show_timeline,omitempty"`
	ScrollLines          *uint8  `json:"scroll_lines,omitempty"`
	InvertScroll         bool    `json:"invert_scroll,omitempty"`
	PromptSuggestions    bool    `json:"prompt_suggestions"`
	PermissionMode       string  `json:"permission_mode"`
}

type DashboardConfig struct {
	Pinned  []string `json:"pinned,omitempty" toml:"pinned"`
	Reorder []string `json:"reorder,omitempty" toml:"reorder"`
}

type GoalConfig struct {
	VerifierCount       int             `json:"verifier_count"`
	ClassifierMaxRuns   uint32          `json:"classifier_max_runs"`
	ReverifyAfter       uint32          `json:"reverify_after"`
	PlannerEnabled      bool            `json:"planner_enabled,omitempty"`
	SummaryEnabled      bool            `json:"summary_enabled,omitempty"`
	StrategistEvery     uint32          `json:"strategist_every,omitempty"`
	UseCurrentModelOnly bool            `json:"use_current_model_only,omitempty"`
	PlannerModel        *GoalRoleModel  `json:"planner_model,omitempty"`
	StrategistModel     *GoalRoleModel  `json:"strategist_model,omitempty"`
	SkepticModels       []GoalRoleModel `json:"skeptic_models,omitempty"`
}

func (c Config) GoalPlannerEnabled(goalEnabled bool) bool {
	if c.goalPlannerResolved {
		return c.Goal.PlannerEnabled
	}
	return goalEnabled
}

func (c Config) GoalSummaryEnabled(goalEnabled bool) bool {
	if c.goalSummaryResolved {
		return c.Goal.SummaryEnabled
	}
	return goalEnabled
}

func (c *Config) OverrideMemory(enabled bool) {
	c.Memory.Enabled = enabled
	c.memoryConfigured = true
}

type GoalRoleModel struct {
	Model     string `json:"model" toml:"model"`
	AgentType string `json:"agent_type" toml:"agent_type"`
}

func (m *GoalRoleModel) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) == nil {
		_ = json.Unmarshal(raw["model"], &m.Model)
		_ = json.Unmarshal(raw["agent_type"], &m.AgentType)
	}
	return nil
}

func (m *GoalRoleModel) UnmarshalTOML(data []byte) error {
	var raw struct {
		Model     string `toml:"model"`
		AgentType string `toml:"agent_type"`
	}
	if toml.Unmarshal(data, &raw) == nil {
		m.Model, m.AgentType = raw.Model, raw.AgentType
	}
	return nil
}

func (m GoalRoleModel) valid() bool {
	return strings.TrimSpace(m.Model) != "" && strings.TrimSpace(m.AgentType) != ""
}

func normalizeGoalRoleModels(models []GoalRoleModel) []GoalRoleModel {
	result := make([]GoalRoleModel, 0, len(models))
	for _, model := range models {
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		if model.valid() {
			result = append(result, model)
		}
	}
	return result
}

func (c Config) GoalStrategistEvery() uint32 {
	if c.Goal.StrategistEvery > 0 {
		return c.Goal.StrategistEvery
	}
	return max(uint32(1), c.Goal.ClassifierMaxRuns/2)
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
	APIKey             string                     `json:"api_key,omitempty" toml:"api_key"`
	BaseURL            string                     `json:"base_url,omitempty" toml:"base_url"`
	Model              string                     `json:"model,omitempty" toml:"model_name"`
	Backend            string                     `json:"backend,omitempty" toml:"backend"`
	SystemPrompt       string                     `json:"system_prompt,omitempty" toml:"system_prompt"`
	MaxSteps           int                        `json:"max_steps,omitempty" toml:"max_steps"`
	Env                map[string]string          `json:"env,omitempty" toml:"env"`
	HTTPTimeout        string                     `json:"http_timeout,omitempty" toml:"http_timeout"`
	MCPServers         map[string]MCPServerConfig `json:"mcp_servers,omitempty" toml:"mcp_servers"`
	DisabledMCPServers []string                   `json:"disabled_mcp_servers,omitempty" toml:"disabled_mcp_servers"`
	DisabledMCPTools   map[string][]string        `json:"disabled_mcp_tools,omitempty" toml:"disabled_mcp_tools"`
	LSPServers         map[string]LSPServerConfig `json:"lsp_servers,omitempty" toml:"lsp_servers"`
	Permission         PermissionConfig           `json:"permission,omitempty" toml:"permission"`
	AutoMode           AutoModeConfig             `json:"auto_mode,omitempty" toml:"auto_mode"`
	Session            sessionConfig              `json:"session,omitempty" toml:"session"`
	ContextWindow      int                        `json:"context_window,omitempty" toml:"context_window"`
	Compaction         fileCompactionConfig       `json:"compaction,omitempty" toml:"compaction"`
	Memory             *fileMemoryConfig          `json:"memory,omitempty" toml:"memory"`
	Compat             fileCompatConfig           `json:"compat,omitempty" toml:"compat"`
	Skills             SkillsConfig               `json:"skills,omitempty" toml:"skills"`
	Plugins            PluginsConfig              `json:"plugins,omitempty" toml:"plugins"`
	Marketplace        MarketplaceConfig          `json:"marketplace,omitempty" toml:"marketplace"`
	Features           struct {
		WebFetch          *bool `json:"web_fetch,omitempty" toml:"web_fetch"`
		AutoWake          *bool `json:"auto_wake,omitempty" toml:"auto_wake"`
		Feedback          *bool `json:"feedback,omitempty" toml:"feedback"`
		TwoPassCompaction *bool `json:"two_pass_compaction,omitempty" toml:"two_pass_compaction"`
	} `json:"features,omitempty" toml:"features"`
	AuthProviderCommand string            `json:"auth_provider_command,omitempty" toml:"auth_provider_command"`
	AuthTokenTTL        int64             `json:"auth_token_ttl,omitempty" toml:"auth_token_ttl"`
	GrokComConfig       fileGrokComConfig `json:"grok_com_config,omitempty" toml:"grok_com_config"`
	Auth                fileAuthConfig    `json:"auth,omitempty" toml:"auth"`
	Models              struct {
		Default        string   `toml:"default"`
		WebSearch      string   `toml:"web_search"`
		AllowedModels  []string `toml:"allowed_models"`
		HiddenModels   []string `toml:"hidden_models"`
		DisabledModels []string `toml:"disabled_models"`
	} `json:"-" toml:"models"`
	ModelEntries map[string]modelConfig `json:"-" toml:"model"`
	Toolset      struct {
		FileToolset     string                    `json:"file_toolset,omitempty" toml:"file_toolset"`
		Hashline        fileHashlineConfig        `json:"hashline,omitempty" toml:"hashline"`
		WebFetch        fileWebFetchConfig        `json:"web_fetch,omitempty" toml:"web_fetch"`
		AskUserQuestion fileAskUserQuestionConfig `json:"ask_user_question,omitempty" toml:"ask_user_question"`
	} `json:"toolset,omitempty" toml:"toolset"`
	Goal        fileGoalConfig        `json:"goal,omitempty" toml:"goal"`
	UI          fileUIConfig          `json:"ui,omitempty" toml:"ui"`
	Dashboard   DashboardConfig       `json:"dashboard,omitempty" toml:"dashboard"`
	Endpoints   fileEndpointsConfig   `json:"endpoints,omitempty" toml:"endpoints"`
	FolderTrust fileFolderTrustConfig `json:"folder_trust,omitempty" toml:"folder_trust"`
}

type fileHashlineConfig struct {
	Scheme    *string `json:"scheme,omitempty" toml:"scheme"`
	HashLen   *int    `json:"hash_len,omitempty" toml:"hash_len"`
	ChunkSize *int    `json:"chunk_size,omitempty" toml:"chunk_size"`
}

type fileUIConfig struct {
	Theme                        *string `json:"theme,omitempty" toml:"theme"`
	KeepTextSelection            any     `json:"keep_text_selection,omitempty" toml:"keep_text_selection"`
	WordSeparators               *string `json:"word_separators,omitempty" toml:"word_separators"`
	MouseReportingToggle         *bool   `json:"mouse_reporting_toggle,omitempty" toml:"mouse_reporting_toggle"`
	VimMode                      *bool   `json:"vim_mode,omitempty" toml:"vim_mode"`
	CompactMode                  *bool   `json:"compact_mode,omitempty" toml:"compact_mode"`
	ShowTimestamps               *bool   `json:"show_timestamps,omitempty" toml:"show_timestamps"`
	ShowTimeline                 *bool   `json:"show_timeline,omitempty" toml:"show_timeline"`
	ScrollLines                  *uint8  `json:"scroll_lines,omitempty" toml:"scroll_lines"`
	InvertScroll                 *bool   `json:"invert_scroll,omitempty" toml:"invert_scroll"`
	PromptSuggestions            *bool   `json:"prompt_suggestions,omitempty" toml:"prompt_suggestions"`
	PermissionMode               *string `json:"permission_mode,omitempty" toml:"permission_mode"`
	SelectionHighlightDurationMS *uint64 `json:"selection_highlight_duration_ms,omitempty" toml:"selection_highlight_duration_ms"`
	DoubleClickAction            *string `json:"double_click_action,omitempty" toml:"double_click_action"`
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
	Permission *PermissionConfig `toml:"permission"`
	AutoMode   *AutoModeConfig   `toml:"auto_mode"`
	Features   struct {
		Feedback *bool `toml:"feedback"`
	} `toml:"features"`
	GrokComConfig *requirementsGrokConfig `toml:"grok_com_config"`
	Auth          *requirementsAuthConfig `toml:"auth"`
	Toolset       struct {
		AskUserQuestion *fileAskUserQuestionConfig `toml:"ask_user_question"`
	} `toml:"toolset"`
	UI struct {
		DisableBypassPermissionsMode any `toml:"disable_bypass_permissions_mode"`
		Yolo                         any `toml:"yolo"`
	} `toml:"ui"`
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

type fileGoalConfig struct {
	VerifierCount       *int            `json:"verifier_count,omitempty" toml:"verifier_count"`
	ClassifierMaxRuns   *uint32         `json:"classifier_max_runs,omitempty" toml:"classifier_max_runs"`
	ReverifyAfter       *uint32         `json:"reverify_after,omitempty" toml:"reverify_after"`
	PlannerEnabled      *bool           `json:"planner_enabled,omitempty" toml:"planner_enabled"`
	SummaryEnabled      *bool           `json:"summary_enabled,omitempty" toml:"summary_enabled"`
	StrategistEvery     *uint32         `json:"strategist_every,omitempty" toml:"strategist_every"`
	UseCurrentModelOnly *bool           `json:"use_current_model_only,omitempty" toml:"use_current_model_only"`
	PlannerModel        *GoalRoleModel  `json:"planner_model,omitempty" toml:"planner_model"`
	StrategistModel     *GoalRoleModel  `json:"strategist_model,omitempty" toml:"strategist_model"`
	SkepticModels       []GoalRoleModel `json:"skeptic_models,omitempty" toml:"skeptic_models"`
}

type modelConfig struct {
	Model                       string `toml:"model"`
	Name                        string `toml:"name"`
	Description                 string `toml:"description"`
	Hidden                      *bool  `toml:"hidden"`
	BaseURL                     string `toml:"base_url"`
	APIKey                      string `toml:"api_key"`
	Backend                     string `toml:"backend"`
	EnvKey                      any    `toml:"env_key"`
	ContextWindow               int    `toml:"context_window"`
	AutoCompactThresholdPercent *int   `toml:"auto_compact_threshold_percent"`
	ReasoningEffort             string `toml:"reasoning_effort"`
	SupportsReasoningEffort     *bool  `toml:"supports_reasoning_effort"`
	ReasoningEfforts            []any  `toml:"reasoning_efforts"`
}

type sessionConfig struct {
	AutoCompactThresholdPercent *int `json:"auto_compact_threshold_percent,omitempty" toml:"auto_compact_threshold_percent"`
}

type fileCompactionConfig struct {
	Pruning     filePruningConfig      `json:"pruning,omitempty" toml:"pruning"`
	MemoryFlush *fileMemoryFlushConfig `json:"memory_flush,omitempty" toml:"memory_flush"`
}

type fileMemoryConfig struct {
	Enabled          *bool                             `json:"enabled,omitempty" toml:"enabled"`
	InitialInjection *fileMemoryInitialInjectionConfig `json:"initial_injection,omitempty" toml:"initial_injection"`
	Session          *fileMemorySessionConfig          `json:"session,omitempty" toml:"session"`
	Index            *fileMemoryIndexConfig            `json:"index,omitempty" toml:"index"`
	Search           *fileMemorySearchConfig           `json:"search,omitempty" toml:"search"`
	GC               *fileMemoryGCConfig               `json:"gc,omitempty" toml:"gc"`
	Dream            *fileMemoryDreamConfig            `json:"dream,omitempty" toml:"dream"`
}

type fileMemoryIndexConfig struct {
	MaxChunkChars     *int `json:"max_chunk_chars,omitempty" toml:"max_chunk_chars"`
	ChunkOverlapChars *int `json:"chunk_overlap_chars,omitempty" toml:"chunk_overlap_chars"`
}

type fileMemorySearchConfig struct {
	MaxResults    *int                           `json:"max_results,omitempty" toml:"max_results"`
	MinScore      *float64                       `json:"min_score,omitempty" toml:"min_score"`
	RecencyDecay  *float64                       `json:"recency_decay,omitempty" toml:"recency_decay"`
	TemporalDecay *fileMemoryTemporalDecayConfig `json:"temporal_decay,omitempty" toml:"temporal_decay"`
	MMR           *fileMemoryMMRConfig           `json:"mmr,omitempty" toml:"mmr"`
	SourceWeights map[string]float64             `json:"source_weights,omitempty" toml:"source_weights"`
}

type fileMemoryTemporalDecayConfig struct {
	Enabled      *bool    `json:"enabled,omitempty" toml:"enabled"`
	HalfLifeDays *float64 `json:"half_life_days,omitempty" toml:"half_life_days"`
}

type fileMemoryMMRConfig struct {
	Enabled *bool    `json:"enabled,omitempty" toml:"enabled"`
	Lambda  *float64 `json:"lambda,omitempty" toml:"lambda"`
}

type fileMemoryGCConfig struct {
	MaxAgeDays *uint64 `json:"max_age_days,omitempty" toml:"max_age_days"`
}

type fileMemoryDreamConfig struct {
	Enabled              *bool   `json:"enabled,omitempty" toml:"enabled"`
	MinHours             *uint64 `json:"min_hours,omitempty" toml:"min_hours"`
	MinSessions          *uint64 `json:"min_sessions,omitempty" toml:"min_sessions"`
	StaleLockSeconds     *uint64 `json:"stale_lock_secs,omitempty" toml:"stale_lock_secs"`
	CheckIntervalSeconds *uint64 `json:"check_interval_secs,omitempty" toml:"check_interval_secs"`
}

type fileMemoryInitialInjectionConfig struct {
	Enabled  *bool    `json:"enabled,omitempty" toml:"enabled"`
	MinScore *float64 `json:"min_score,omitempty" toml:"min_score"`
}

type fileMemorySessionConfig struct {
	SaveOnEnd *bool `json:"save_on_end,omitempty" toml:"save_on_end"`
}

type fileMemoryFlushConfig struct {
	Enabled             *bool   `json:"enabled,omitempty" toml:"enabled"`
	SoftThresholdTokens *int    `json:"soft_threshold_tokens,omitempty" toml:"soft_threshold_tokens"`
	FlushModel          *string `json:"flush_model,omitempty" toml:"flush_model"`
	MaxFlushWriteChars  *int    `json:"max_flush_write_chars,omitempty" toml:"max_flush_write_chars"`
	IdleTimeoutSeconds  *uint64 `json:"idle_timeout_secs,omitempty" toml:"idle_timeout_secs"`
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
		FeedbackEnabled:             true,
		AskUserQuestion:             AskUserQuestionConfig{TimeoutEnabled: true, TimeoutSeconds: 30 * 60},
		Toolset:                     ToolsetConfig{FileToolset: "standard", Hashline: HashlineConfig{Scheme: "chunk", HashLen: 3, ChunkSize: 8}},
		Goal:                        GoalConfig{VerifierCount: 3, ClassifierMaxRuns: 10, ReverifyAfter: 8},
		UI:                          UIConfig{Theme: "groknight", KeepTextSelection: "flash", ShowTimestamps: true, PromptSuggestions: true, PermissionMode: "ask"},
		Pruning:                     PruningConfig{Enabled: true, KeepLastNTurns: 3, SoftTrimThreshold: 4000, SoftTrimHead: 1500, SoftTrimTail: 1500, HardClearAgeTurns: 10},
		Memory:                      memory.DefaultConfig(),
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
	if err := mergeModelProfiles(cfg, disk.ModelEntries); err != nil {
		return err
	}
	if err := validateModelPatterns("allowed_models", disk.Models.AllowedModels); err != nil {
		return err
	}
	if err := validateModelPatterns("hidden_models", disk.Models.HiddenModels); err != nil {
		return err
	}
	if err := validateModelPatterns("disabled_models", disk.Models.DisabledModels); err != nil {
		return err
	}
	if disk.Models.AllowedModels != nil {
		cfg.AllowedModels = append([]string(nil), disk.Models.AllowedModels...)
		cfg.allowedModelsConfigured = true
	}
	if disk.Models.HiddenModels != nil {
		cfg.HiddenModels = append([]string(nil), disk.Models.HiddenModels...)
		cfg.hiddenModelsConfigured = true
	}
	if disk.Models.DisabledModels != nil {
		cfg.DisabledModels = append([]string(nil), disk.Models.DisabledModels...)
		cfg.disabledModelsConfigured = true
	}
	if disk.Models.Default != "" {
		cfg.DefaultModelID = disk.Models.Default
		cfg.defaultModelConfigured = true
	}
	if disk.Model != "" {
		cfg.modelConfigured = true
	}
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
	if profile, ok := cfg.ModelProfiles[cfg.DefaultModelID]; ok {
		cfg.ReasoningEffort = profile.ReasoningEffort
		cfg.ModelSupportsReasoningEffort = profile.SupportsReasoningEffort
		cfg.ModelReasoningEfforts = append([]ReasoningEffortOption(nil), profile.ReasoningEfforts...)
	}
	if disk.SystemPrompt != "" {
		cfg.SystemPrompt = disk.SystemPrompt
	}
	if disk.MaxSteps > 0 {
		cfg.MaxSteps = disk.MaxSteps
	}
	if disk.Env != nil {
		if cfg.Env == nil {
			cfg.Env = make(map[string]string)
		}
		for key, value := range disk.Env {
			cfg.Env[key] = value
		}
	}
	if disk.MCPServers != nil {
		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]MCPServerConfig)
		}
		for name, server := range disk.MCPServers {
			cfg.MCPServers[name] = server
		}
	}
	if disk.DisabledMCPServers != nil {
		cfg.DisabledMCPServers = append([]string(nil), disk.DisabledMCPServers...)
	}
	if disk.DisabledMCPTools != nil {
		cfg.DisabledMCPTools = cloneStringSlices(disk.DisabledMCPTools)
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
	if err := applyAutoModeConfig(&cfg.AutoMode, disk.AutoMode); err != nil {
		return err
	}
	cfg.autoModeEnabledConfigured = cfg.autoModeEnabledConfigured || disk.AutoMode.Enabled != nil
	cfg.autoModePromptConfigured = cfg.autoModePromptConfigured || strings.TrimSpace(disk.AutoMode.PromptType) != ""
	cfg.autoModeModelConfigured = cfg.autoModeModelConfigured || strings.TrimSpace(disk.AutoMode.ClassifierModel) != ""
	cfg.autoModeReasoningConfigured = cfg.autoModeReasoningConfigured || strings.TrimSpace(disk.AutoMode.ReasoningEffort) != ""
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
	if disk.Features.Feedback != nil {
		cfg.FeedbackEnabled, cfg.feedbackConfigured = *disk.Features.Feedback, true
	}
	if disk.Toolset.WebFetch.AllowedDomains != nil {
		cfg.WebFetch.AllowedDomains = append([]string(nil), disk.Toolset.WebFetch.AllowedDomains...)
		cfg.WebFetch.DomainsConfigured = true
	}
	applyAskUserQuestionConfig(&cfg.AskUserQuestion, disk.Toolset.AskUserQuestion)
	if disk.Toolset.FileToolset != "" {
		cfg.Toolset.FileToolset = strings.TrimSpace(disk.Toolset.FileToolset)
	}
	if disk.Toolset.Hashline.Scheme != nil {
		cfg.Toolset.Hashline.Scheme = strings.TrimSpace(*disk.Toolset.Hashline.Scheme)
	}
	if disk.Toolset.Hashline.HashLen != nil {
		cfg.Toolset.Hashline.HashLen = *disk.Toolset.Hashline.HashLen
	}
	if disk.Toolset.Hashline.ChunkSize != nil {
		cfg.Toolset.Hashline.ChunkSize = *disk.Toolset.Hashline.ChunkSize
	}
	if disk.Toolset.FileToolset != "" && cfg.Toolset.FileToolset != "standard" && cfg.Toolset.FileToolset != "hashline" {
		return errors.New("toolset file_toolset must be standard or hashline")
	}
	if disk.Toolset.Hashline.Scheme != nil && cfg.Toolset.Hashline.Scheme != "chunk" && cfg.Toolset.Hashline.Scheme != "content_only" {
		return errors.New("toolset hashline scheme must be chunk or content_only")
	}
	if disk.Toolset.Hashline.HashLen != nil && (cfg.Toolset.Hashline.HashLen < 1 || cfg.Toolset.Hashline.HashLen > 4) {
		return errors.New("toolset hashline hash_len must be between 1 and 4")
	}
	if disk.Toolset.Hashline.ChunkSize != nil && cfg.Toolset.Hashline.Scheme == "chunk" && cfg.Toolset.Hashline.ChunkSize < 1 {
		return errors.New("toolset hashline chunk_size must be greater than zero")
	}
	if disk.UI.Theme != nil {
		canonical, ok := theme.Canonical(*disk.UI.Theme)
		if !ok {
			return errors.New("ui theme must be auto, groknight, grokday, tokyonight, rosepine-moon, or oscura-midnight")
		}
		cfg.UI.Theme = canonical
	}
	if disk.UI.KeepTextSelection != nil {
		value, err := parseKeepTextSelection(disk.UI.KeepTextSelection)
		if err != nil {
			return err
		}
		cfg.uiTextSelectionConfigured = value == "flash" || value == "hold" || value == "word_select"
		cfg.uiTextSelectionInvalid = !cfg.uiTextSelectionConfigured
		if cfg.uiTextSelectionConfigured {
			cfg.UI.KeepTextSelection = value
		}
	}
	if disk.UI.SelectionHighlightDurationMS != nil {
		value := *disk.UI.SelectionHighlightDurationMS
		cfg.uiSelectionHighlightDuration = &value
	}
	if disk.UI.DoubleClickAction != nil {
		value := *disk.UI.DoubleClickAction
		cfg.uiDoubleClickAction = &value
	}
	if !cfg.uiTextSelectionConfigured {
		cfg.UI.KeepTextSelection = "flash"
		if cfg.uiDoubleClickAction != nil && *cfg.uiDoubleClickAction == "word_select" {
			cfg.UI.KeepTextSelection = "word_select"
		} else if !cfg.uiTextSelectionInvalid && cfg.uiSelectionHighlightDuration != nil && *cfg.uiSelectionHighlightDuration == 0 {
			cfg.UI.KeepTextSelection = "hold"
		}
	}
	if disk.UI.WordSeparators != nil {
		value := *disk.UI.WordSeparators
		cfg.UI.WordSeparators = &value
	}
	if disk.UI.MouseReportingToggle != nil {
		cfg.UI.MouseReportingToggle = *disk.UI.MouseReportingToggle
	}
	if disk.UI.VimMode != nil {
		cfg.UI.VimMode = *disk.UI.VimMode
	}
	if disk.UI.CompactMode != nil {
		cfg.UI.CompactMode = *disk.UI.CompactMode
	}
	if disk.UI.ShowTimestamps != nil {
		cfg.UI.ShowTimestamps = *disk.UI.ShowTimestamps
	}
	if disk.UI.ShowTimeline != nil {
		cfg.UI.ShowTimeline = *disk.UI.ShowTimeline
	}
	if disk.UI.ScrollLines != nil {
		cfg.UI.ScrollLines = normalizedScrollLines(*disk.UI.ScrollLines)
	}
	if disk.UI.InvertScroll != nil {
		cfg.UI.InvertScroll = *disk.UI.InvertScroll
	}
	if disk.UI.PromptSuggestions != nil {
		cfg.UI.PromptSuggestions = *disk.UI.PromptSuggestions
	}
	if disk.UI.PermissionMode != nil {
		mode, err := normalizePermissionMode(*disk.UI.PermissionMode)
		if err != nil {
			return err
		}
		cfg.UI.PermissionMode = mode
		cfg.uiPermissionModeConfigured = true
	}
	if disk.Dashboard.Pinned != nil {
		cfg.Dashboard.Pinned = cleanDashboardSessionIDs(disk.Dashboard.Pinned)
	}
	if disk.Dashboard.Reorder != nil {
		cfg.Dashboard.Reorder = cleanDashboardSessionOrder(disk.Dashboard.Reorder)
	}
	if disk.Goal.VerifierCount != nil {
		cfg.Goal.VerifierCount = normalizedGoalVerifierCount(*disk.Goal.VerifierCount)
		cfg.goalVerifierConfigured = true
	}
	if disk.Goal.ClassifierMaxRuns != nil {
		cfg.Goal.ClassifierMaxRuns = max(uint32(1), *disk.Goal.ClassifierMaxRuns)
		cfg.goalClassifierMaxConfigured = true
	}
	if disk.Goal.ReverifyAfter != nil {
		cfg.Goal.ReverifyAfter = max(uint32(1), *disk.Goal.ReverifyAfter)
	}
	if disk.Goal.PlannerEnabled != nil {
		cfg.Goal.PlannerEnabled = *disk.Goal.PlannerEnabled
		cfg.goalPlannerConfigured = true
		cfg.goalPlannerResolved = true
	}
	if disk.Goal.SummaryEnabled != nil {
		cfg.Goal.SummaryEnabled = *disk.Goal.SummaryEnabled
		cfg.goalSummaryConfigured = true
		cfg.goalSummaryResolved = true
	}
	if disk.Goal.StrategistEvery != nil {
		cfg.Goal.StrategistEvery = max(uint32(1), *disk.Goal.StrategistEvery)
		cfg.goalStrategistEveryConfigured = true
	}
	if disk.Goal.UseCurrentModelOnly != nil {
		cfg.Goal.UseCurrentModelOnly = *disk.Goal.UseCurrentModelOnly
	}
	if disk.Goal.PlannerModel != nil && disk.Goal.PlannerModel.valid() {
		model := *disk.Goal.PlannerModel
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		cfg.Goal.PlannerModel = &model
	}
	if disk.Goal.StrategistModel != nil && disk.Goal.StrategistModel.valid() {
		model := *disk.Goal.StrategistModel
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		cfg.Goal.StrategistModel = &model
	}
	cfg.Goal.SkepticModels = normalizeGoalRoleModels(disk.Goal.SkepticModels)
	if disk.ContextWindow > 0 {
		cfg.ContextWindow = disk.ContextWindow
	}
	if disk.Session.AutoCompactThresholdPercent != nil {
		cfg.AutoCompactThresholdPercent = *disk.Session.AutoCompactThresholdPercent
	}
	if disk.Features.TwoPassCompaction != nil {
		cfg.TwoPassCompaction = *disk.Features.TwoPassCompaction
		cfg.twoPassCompactionConfigured = true
	}
	applyPruningConfig(&cfg.Pruning, disk.Compaction.Pruning)
	applyMemoryConfig(cfg, disk.Memory, disk.Compaction.MemoryFlush)
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

func mergeModelProfiles(cfg *Config, entries map[string]modelConfig) error {
	if len(entries) == 0 {
		return nil
	}
	if cfg.ModelProfiles == nil {
		cfg.ModelProfiles = make(map[string]ModelProfile)
	}
	for name, entry := range entries {
		profile := cfg.ModelProfiles[name]
		if entry.Model != "" {
			profile.Model = entry.Model
		}
		if entry.Name != "" {
			profile.Name = entry.Name
		}
		if entry.Description != "" {
			profile.Description = entry.Description
		}
		if entry.Hidden != nil {
			profile.Hidden = *entry.Hidden
			profile.hiddenConfigured = true
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
		if entry.ReasoningEffort != "" {
			profile.ReasoningEffort = entry.ReasoningEffort
		}
		if entry.SupportsReasoningEffort != nil {
			profile.SupportsReasoningEffort = *entry.SupportsReasoningEffort
			profile.supportsReasoningConfigured = true
		}
		if entry.ReasoningEfforts != nil {
			options, err := parseReasoningEffortOptions(entry.ReasoningEfforts)
			if err != nil {
				return fmt.Errorf("invalid model.%s reasoning_efforts: %w", name, err)
			}
			profile.ReasoningEfforts = options
		}
		normalized, err := normalizeModelProfile(name, profile)
		if err != nil {
			return err
		}
		cfg.ModelProfiles[name] = normalized
	}
	return nil
}

func (c Config) ResolveModel(slug string) (Config, bool) {
	_, resolved, ok := c.ResolveModelEntry(slug)
	return resolved, ok
}

func (c Config) ResolveModelEntry(slug string) (string, Config, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", Config{}, false
	}
	name, profile, ok := c.modelProfileWithDefault(slug)
	if !ok {
		if slug == c.Model {
			id := c.DefaultModelID
			if id == "" {
				id = slug
			}
			if c.modelFiltered(c.DisabledModels, id, slug) {
				return "", Config{}, false
			}
			return id, c, true
		}
		return "", Config{}, false
	}
	if c.modelFiltered(c.DisabledModels, name, profile.Model) {
		return "", Config{}, false
	}
	result := c
	result.DefaultModelID = name
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
	result.ReasoningEffort = profile.ReasoningEffort
	result.ModelSupportsReasoningEffort = profile.SupportsReasoningEffort
	result.ModelReasoningEfforts = append([]ReasoningEffortOption(nil), profile.ReasoningEfforts...)
	return name, result, true
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

// ReloadModelCatalog applies model-only config changes without disturbing live
// credentials or unrelated session settings. Remote filters remain fail-closed
// unless the local config explicitly owns that field.
func (c *Config) ReloadModelCatalog(next Config) {
	c.ModelProfiles = cloneModelProfiles(next.ModelProfiles)
	c.Model = next.Model
	if next.defaultModelConfigured || c.defaultModelConfigured {
		c.DefaultModelID = next.DefaultModelID
	}
	if next.allowedModelsConfigured || c.allowedModelsConfigured {
		c.AllowedModels = append([]string(nil), next.AllowedModels...)
	}
	if next.hiddenModelsConfigured || c.hiddenModelsConfigured {
		c.HiddenModels = append([]string(nil), next.HiddenModels...)
	}
	if next.disabledModelsConfigured || c.disabledModelsConfigured {
		c.DisabledModels = append([]string(nil), next.DisabledModels...)
	}
	c.modelConfigured = next.modelConfigured
	c.defaultModelConfigured = next.defaultModelConfigured
	c.allowedModelsConfigured = next.allowedModelsConfigured
	c.hiddenModelsConfigured = next.hiddenModelsConfigured
	c.disabledModelsConfigured = next.disabledModelsConfigured
	c.ReasoningEffort = next.ReasoningEffort
	c.ModelSupportsReasoningEffort = next.ModelSupportsReasoningEffort
	c.ModelReasoningEfforts = append([]ReasoningEffortOption(nil), next.ModelReasoningEfforts...)
}

func (c Config) HasExplicitModelPreference() bool {
	return c.defaultModelConfigured || c.modelConfigured
}

func cloneModelProfiles(source map[string]ModelProfile) map[string]ModelProfile {
	if source == nil {
		return nil
	}
	cloned := make(map[string]ModelProfile, len(source))
	for id, profile := range source {
		profile.ReasoningEfforts = append([]ReasoningEffortOption(nil), profile.ReasoningEfforts...)
		cloned[id] = profile
	}
	return cloned
}

func (c Config) modelProfileWithDefault(slug string) (string, ModelProfile, bool) {
	if profile, ok := c.ModelProfiles[slug]; ok {
		return slug, profile, true
	}
	if slug == c.Model && c.DefaultModelID != "" {
		if profile, ok := c.ModelProfiles[c.DefaultModelID]; ok {
			return c.DefaultModelID, profile, true
		}
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

func (c Config) ModelSelectable(id, model string) bool {
	return !c.modelFiltered(c.DisabledModels, id, model) &&
		(len(c.AllowedModels) == 0 || c.modelFiltered(c.AllowedModels, id, model))
}

func (c Config) ModelVisible(id, model string) bool {
	profile, configured := c.ModelProfiles[id]
	return c.ModelSelectable(id, model) && !(configured && profile.Hidden) && !c.modelFiltered(c.HiddenModels, id, model)
}

func (c Config) modelFiltered(patterns []string, id, model string) bool {
	for _, pattern := range patterns {
		matchedID, _ := path.Match(pattern, id)
		matchedModel, _ := path.Match(pattern, model)
		if matchedID || matchedModel {
			return true
		}
	}
	return false
}

func validateModelPatterns(name string, patterns []string) error {
	for _, pattern := range patterns {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid models.%s pattern %q: %w", name, pattern, err)
		}
	}
	return nil
}

func normalizeModelProfile(name string, profile ModelProfile) (ModelProfile, error) {
	profile.ReasoningEffort = normalizeReasoningEffort(profile.ReasoningEffort)
	if profile.ReasoningEffort == "invalid" {
		return ModelProfile{}, fmt.Errorf("invalid model.%s reasoning_effort", name)
	}
	for index := range profile.ReasoningEfforts {
		option := &profile.ReasoningEfforts[index]
		option.Value = normalizeReasoningEffort(option.Value)
		if option.Value == "" || option.Value == "invalid" {
			return ModelProfile{}, fmt.Errorf("invalid model.%s reasoning_efforts value", name)
		}
		if option.ID == "" {
			option.ID = option.Value
		}
		if option.Label == "" {
			option.Label = strings.ToUpper(option.ID[:1]) + option.ID[1:]
		}
		if profile.ReasoningEffort == "" && option.Default {
			profile.ReasoningEffort = option.Value
		}
	}
	profile.SupportsReasoningEffort = profile.SupportsReasoningEffort || profile.ReasoningEffort != "" || len(profile.ReasoningEfforts) > 0
	return profile, nil
}

func parseReasoningEffortOptions(raw []any) ([]ReasoningEffortOption, error) {
	options := make([]ReasoningEffortOption, 0, len(raw))
	for _, item := range raw {
		switch value := item.(type) {
		case string:
			options = append(options, ReasoningEffortOption{Value: value})
		case map[string]any:
			option := ReasoningEffortOption{}
			option.ID, _ = value["id"].(string)
			option.Value, _ = value["value"].(string)
			option.Label, _ = value["label"].(string)
			option.Description, _ = value["description"].(string)
			option.Default, _ = value["default"].(bool)
			options = append(options, option)
		default:
			return nil, errors.New("option must be a string or table")
		}
	}
	return options, nil
}

func normalizeReasoningEffort(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "":
		return ""
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return value
	case "max":
		return "xhigh"
	default:
		return "invalid"
	}
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

func applyMemoryConfig(cfg *Config, source *fileMemoryConfig, flush *fileMemoryFlushConfig) {
	if source != nil {
		cfg.memoryConfigured = true
		if source.Enabled != nil {
			cfg.Memory.Enabled = *source.Enabled
		}
		if source.InitialInjection != nil {
			cfg.memoryInjectionConfigured = true
			if source.InitialInjection.Enabled != nil {
				cfg.Memory.InitialInjection = *source.InitialInjection.Enabled
			}
			if source.InitialInjection.MinScore != nil {
				value := min(1, max(0, *source.InitialInjection.MinScore))
				cfg.Memory.InitialInjectionMinScore = &value
			}
		}
		if source.Session != nil && source.Session.SaveOnEnd != nil {
			cfg.Memory.SaveOnEnd = *source.Session.SaveOnEnd
		}
		if source.Index != nil {
			if source.Index.MaxChunkChars != nil {
				cfg.Memory.Index.MaxChunkChars = *source.Index.MaxChunkChars
			}
			if source.Index.ChunkOverlapChars != nil {
				cfg.Memory.Index.ChunkOverlapChars = *source.Index.ChunkOverlapChars
			}
		}
		if source.Search != nil {
			cfg.memorySearchConfigured = true
			if source.Search.MaxResults != nil {
				cfg.Memory.Search.MaxResults = *source.Search.MaxResults
			}
			if source.Search.MinScore != nil {
				cfg.Memory.Search.MinScore = *source.Search.MinScore
			}
			if source.Search.RecencyDecay != nil {
				cfg.Memory.Search.RecencyDecay = *source.Search.RecencyDecay
			}
			if source.Search.TemporalDecay != nil {
				if source.Search.TemporalDecay.Enabled != nil {
					cfg.Memory.Search.TemporalDecay.Enabled = *source.Search.TemporalDecay.Enabled
				}
				if source.Search.TemporalDecay.HalfLifeDays != nil {
					cfg.Memory.Search.TemporalDecay.HalfLifeDays = *source.Search.TemporalDecay.HalfLifeDays
				}
			}
			if source.Search.MMR != nil {
				if source.Search.MMR.Enabled != nil {
					cfg.Memory.Search.MMR.Enabled = *source.Search.MMR.Enabled
				}
				if source.Search.MMR.Lambda != nil {
					cfg.Memory.Search.MMR.Lambda = min(1, max(0, *source.Search.MMR.Lambda))
				}
			}
			if source.Search.SourceWeights != nil {
				cfg.Memory.Search.SourceWeights = source.Search.SourceWeights
			}
		}
		if source.GC != nil && source.GC.MaxAgeDays != nil {
			cfg.Memory.GC.MaxAgeDays = *source.GC.MaxAgeDays
		}
		if source.Dream != nil {
			cfg.memoryDreamConfigured = true
			if source.Dream.Enabled != nil {
				cfg.Memory.Dream.Enabled = *source.Dream.Enabled
			}
			if source.Dream.MinHours != nil {
				cfg.Memory.Dream.MinHours = *source.Dream.MinHours
			}
			if source.Dream.MinSessions != nil {
				cfg.Memory.Dream.MinSessions = *source.Dream.MinSessions
			}
			if source.Dream.StaleLockSeconds != nil {
				cfg.Memory.Dream.StaleLockSeconds = *source.Dream.StaleLockSeconds
			}
			if source.Dream.CheckIntervalSeconds != nil {
				value := *source.Dream.CheckIntervalSeconds
				cfg.Memory.Dream.CheckIntervalSeconds = &value
			}
		}
	}
	if flush != nil {
		cfg.memoryFlushConfigured = true
		if flush.Enabled != nil {
			cfg.Memory.Flush.Enabled = *flush.Enabled
		}
		if flush.SoftThresholdTokens != nil {
			cfg.Memory.Flush.SoftThresholdTokens = *flush.SoftThresholdTokens
		}
		if flush.FlushModel != nil {
			cfg.Memory.Flush.Model = strings.TrimSpace(*flush.FlushModel)
		}
		if flush.MaxFlushWriteChars != nil {
			cfg.Memory.Flush.MaxWriteChars = *flush.MaxFlushWriteChars
		}
		if flush.IdleTimeoutSeconds != nil {
			value := *flush.IdleTimeoutSeconds
			cfg.Memory.Flush.IdleTimeoutSeconds = &value
		}
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

func normalizedGoalVerifierCount(count int) int {
	return max(1, min(5, count))
}

func applyAutoModeConfig(target *AutoModeConfig, source AutoModeConfig) error {
	normalized, err := normalizeAutoModeConfig(source)
	if err != nil {
		return err
	}
	if normalized.Enabled != nil {
		value := *normalized.Enabled
		target.Enabled = &value
	}
	if normalized.PromptType != "" {
		target.PromptType = normalized.PromptType
	}
	if normalized.ClassifierModel != "" {
		target.ClassifierModel = normalized.ClassifierModel
	}
	if normalized.ReasoningEffort != "" {
		target.ReasoningEffort = normalized.ReasoningEffort
	}
	return nil
}

func normalizeAutoModeConfig(value AutoModeConfig) (AutoModeConfig, error) {
	value.PromptType = strings.ToLower(strings.TrimSpace(value.PromptType))
	value.ClassifierModel = strings.TrimSpace(value.ClassifierModel)
	value.ReasoningEffort = strings.ToLower(strings.TrimSpace(value.ReasoningEffort))
	if value.PromptType != "" {
		switch value.PromptType {
		case "full", "no_user_tool_prefix", "bare_instructions", "just_command":
		default:
			return AutoModeConfig{}, fmt.Errorf("invalid auto_mode prompt_type %q", value.PromptType)
		}
	}
	if value.ReasoningEffort != "" {
		switch value.ReasoningEffort {
		case "none", "minimal", "low", "medium", "high", "xhigh":
		default:
			return AutoModeConfig{}, fmt.Errorf("invalid auto_mode reasoning_effort %q", value.ReasoningEffort)
		}
	}
	return value, nil
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
		cfg.DefaultModelID = ""
		cfg.ReasoningEffort = ""
		cfg.ModelSupportsReasoningEffort = false
		cfg.ModelReasoningEfforts = nil
	}
	if value := os.Getenv("GORK_BACKEND"); value != "" {
		cfg.Backend = value
	}
	if value, ok := envBool("GROK_AUTO_PERMISSION_MODE"); ok {
		cfg.AutoMode.Enabled = &value
		cfg.autoModeEnabledConfigured = true
	}
	if value, ok := envBool("GROK_MOUSE_REPORTING_TOGGLE"); ok {
		cfg.UI.MouseReportingToggle = value
	}
	if value, ok := envBool("GROK_PROMPT_SUGGESTIONS"); ok {
		cfg.UI.PromptSuggestions = value
	}
	if raw := strings.TrimSpace(os.Getenv("GROK_SCROLL_LINES")); raw != "" {
		if value, err := strconv.ParseUint(raw, 10, 8); err == nil {
			cfg.UI.ScrollLines = normalizedScrollLines(uint8(value))
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GROK_INVERT_SCROLL")); raw == "1" || raw == "true" {
		cfg.UI.InvertScroll = true
	} else if raw == "0" || raw == "false" {
		cfg.UI.InvertScroll = false
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
	if value := strings.TrimSpace(os.Getenv("GROK_GOAL_VERIFIER_N")); value != "" {
		if count, err := strconv.Atoi(value); err == nil {
			cfg.Goal.VerifierCount = normalizedGoalVerifierCount(count)
			cfg.goalVerifierConfigured = true
		}
	}
	if value := strings.TrimSpace(os.Getenv("GROK_GOAL_CLASSIFIER_MAX")); value != "" {
		if count, err := strconv.ParseUint(value, 10, 32); err == nil {
			cfg.Goal.ClassifierMaxRuns = max(uint32(1), uint32(count))
			cfg.goalClassifierMaxConfigured = true
		}
	}
	if value := strings.TrimSpace(os.Getenv("GROK_GOAL_REVERIFY_AFTER")); value != "" {
		if count, err := strconv.ParseUint(value, 10, 32); err == nil {
			cfg.Goal.ReverifyAfter = max(uint32(1), uint32(count))
		}
	}
	if value, ok := envBool("GROK_GOAL_PLANNER"); ok {
		cfg.Goal.PlannerEnabled = value
		cfg.goalPlannerConfigured = true
		cfg.goalPlannerResolved = true
	}
	if value, ok := envBool("GROK_GOAL_SUMMARY"); ok {
		cfg.Goal.SummaryEnabled = value
		cfg.goalSummaryConfigured = true
		cfg.goalSummaryResolved = true
	}
	if value := strings.TrimSpace(os.Getenv("GROK_GOAL_STRATEGIST_EVERY")); value != "" {
		if count, err := strconv.ParseUint(value, 10, 32); err == nil {
			cfg.Goal.StrategistEvery = max(uint32(1), uint32(count))
			cfg.goalStrategistEveryConfigured = true
		}
	}
	if value, ok := envBool("GROK_GOAL_USE_CURRENT_MODEL_ONLY"); ok {
		cfg.Goal.UseCurrentModelOnly = value
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
	if value, ok := envBool("GROK_TWO_PASS_COMPACTION"); ok {
		cfg.TwoPassCompaction = value
		cfg.twoPassCompactionConfigured = true
	}
	if value, ok := envBool("GROK_MEMORY"); ok {
		cfg.Memory.Enabled = value
		cfg.memoryConfigured = true
	}
	if value, ok := envBool("GROK_MEMORY_FLUSH"); ok {
		cfg.Memory.Flush.Enabled = value
		cfg.memoryFlushConfigured = true
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
	if value, ok := envBool("GROK_FEEDBACK_ENABLED"); ok {
		cfg.FeedbackEnabled, cfg.feedbackConfigured, cfg.feedbackEnvConfigured = value, true, true
	}
	if value, ok := envBool("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER"); ok {
		cfg.OfficialMarketplaceAutoRegister = value
	}
}

func normalizedScrollLines(value uint8) *uint8 {
	if value == 0 {
		return nil
	}
	value = min(value, 10)
	return &value
}

func parseKeepTextSelection(value any) (string, error) {
	switch value := value.(type) {
	case bool:
		if value {
			return "hold", nil
		}
		return "flash", nil
	case string:
		return strings.TrimSpace(value), nil
	default:
		return "", errors.New("ui keep_text_selection must be a boolean or string")
	}
}

func normalizePermissionMode(value string) (string, error) {
	switch value = strings.TrimSpace(value); value {
	case "ask", "auto", "always-approve":
		return value, nil
	case "default":
		return "ask", nil
	default:
		return "", errors.New("ui permission_mode must be ask, auto, always-approve, or default")
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
	if requirement.AutoMode != nil && requirement.AutoMode.Enabled != nil {
		value := *requirement.AutoMode.Enabled
		cfg.AutoMode.Enabled = &value
		cfg.autoModeEnabledConfigured = true
	}
	if requirement.Features.Feedback != nil && !cfg.feedbackEnvConfigured {
		cfg.FeedbackEnabled, cfg.feedbackConfigured = *requirement.Features.Feedback, true
	}
	if requirement.Auth != nil && requirement.Auth.PreferredMethod != nil {
		cfg.PreferredAuthMethod = strings.ToLower(strings.TrimSpace(*requirement.Auth.PreferredMethod))
	}
	if requirement.Toolset.AskUserQuestion != nil {
		applyAskUserQuestionConfig(&cfg.AskUserQuestion, *requirement.Toolset.AskUserQuestion)
	}
	if disabled, ok := requirement.UI.DisableBypassPermissionsMode.(bool); ok && disabled {
		cfg.DisableBypassPermissionsMode = true
	}
	if yolo, ok := requirement.UI.Yolo.(bool); ok && !yolo {
		cfg.DisableBypassPermissionsMode = true
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
	if c.UI.KeepTextSelection != "" && c.UI.KeepTextSelection != "flash" && c.UI.KeepTextSelection != "hold" && c.UI.KeepTextSelection != "word_select" {
		return errors.New("ui keep_text_selection must be flash, hold, or word_select")
	}
	if c.UI.Theme != "" {
		if _, ok := theme.Canonical(c.UI.Theme); !ok {
			return errors.New("ui theme must be auto, groknight, grokday, tokyonight, rosepine-moon, or oscura-midnight")
		}
	}
	if c.Toolset.FileToolset != "" && c.Toolset.FileToolset != "standard" && c.Toolset.FileToolset != "hashline" {
		return errors.New("toolset file_toolset must be standard or hashline")
	}
	if c.Toolset.Hashline.Scheme != "" && c.Toolset.Hashline.Scheme != "chunk" && c.Toolset.Hashline.Scheme != "content_only" {
		return errors.New("toolset hashline scheme must be chunk or content_only")
	}
	if c.Toolset.Hashline.HashLen != 0 && (c.Toolset.Hashline.HashLen < 1 || c.Toolset.Hashline.HashLen > 4) {
		return errors.New("toolset hashline hash_len must be between 1 and 4")
	}
	if c.Toolset.Hashline.Scheme == "chunk" && c.Toolset.Hashline.ChunkSize < 1 {
		return errors.New("toolset hashline chunk_size must be greater than zero")
	}
	if c.Pruning.KeepLastNTurns < 0 || c.Pruning.SoftTrimThreshold < 0 || c.Pruning.SoftTrimHead < 0 || c.Pruning.SoftTrimTail < 0 || c.Pruning.HardClearAgeTurns < 0 {
		return errors.New("compaction pruning values must not be negative")
	}
	if c.Memory.Enabled && c.Memory.Flush.Enabled && (c.Memory.Flush.SoftThresholdTokens < 0 || c.Memory.Flush.MaxWriteChars < 1) {
		return errors.New("memory flush thresholds must be non-negative and max_flush_write_chars must be positive")
	}
	if timeout := c.Memory.Flush.IdleTimeoutSeconds; timeout != nil && *timeout > uint64((1<<63-1)/int64(time.Second)) {
		return errors.New("memory flush idle_timeout_secs is too large")
	}
	if c.Memory.Enabled && (c.Memory.Index.MaxChunkChars < 1 || c.Memory.Index.ChunkOverlapChars < 0 || c.Memory.Index.ChunkOverlapChars >= c.Memory.Index.MaxChunkChars) {
		return errors.New("memory index chunk sizes are invalid")
	}
	if c.Memory.Enabled && (c.Memory.Search.MaxResults < 1 || c.Memory.Search.MinScore < 0 || c.Memory.Search.MinScore > 1) {
		return errors.New("memory search max_results must be positive and min_score must be between 0 and 1")
	}
	if c.Memory.Enabled && (c.Memory.Dream.MinHours > uint64((1<<63-1)/int64(time.Hour)) || c.Memory.Dream.StaleLockSeconds > uint64((1<<63-1)/int64(time.Second))) {
		return errors.New("memory dream duration is too large")
	}
	if interval := c.Memory.Dream.CheckIntervalSeconds; c.Memory.Enabled && interval != nil && *interval > uint64((1<<63-1)/int64(time.Second)) {
		return errors.New("memory dream check_interval_secs is too large")
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
