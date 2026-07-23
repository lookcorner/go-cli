package config

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestLoadGrokTOMLModelAndServers(t *testing.T) {
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CUSTOM_PROVIDER_KEY", "secret-from-env-key")
	t.Setenv("CUSTOM_SEARCH_KEY", "search-secret")
	t.Setenv("GORK_WEB_SEARCH_API_KEY", "")
	t.Setenv("GORK_WEB_SEARCH_BASE_URL", "")
	t.Setenv("GORK_WEB_SEARCH_MODEL", "")
	t.Setenv("GROK_WEB_FETCH_PROXY", "https://env-proxy.example")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[models]
default = "local"
web_search = "search"

[model.local]
model = "provider-model-id"
base_url = "https://provider.example/v1"
backend = "chat_completions"
env_key = ["MISSING_KEY", "CUSTOM_PROVIDER_KEY"]
context_window = 200000
auto_compact_threshold_percent = 80

[model.search]
model = "search-model-id"
base_url = "https://search.example/v1/"
backend = "responses"
env_key = "CUSTOM_SEARCH_KEY"

[session]
auto_compact_threshold_percent = 70

[compaction.pruning]
keep_last_n_turns = 5
soft_trim_threshold = 6000

[compaction.memory_flush]
enabled = true
soft_threshold_tokens = 3000
flush_model = "memory-model"
max_flush_write_chars = 7000
idle_timeout_secs = 300

[memory]
enabled = true

[memory.initial_injection]
enabled = false
min_score = 0.25

[memory.session]
save_on_end = false

[memory.index]
max_chunk_chars = 1200
chunk_overlap_chars = 200

[memory.search]
max_results = 4
min_score = 0.5
recency_decay = 0.9

[memory.search.temporal_decay]
enabled = true
half_life_days = 14

[memory.search.mmr]
enabled = true
lambda = 0.5

[memory.search.source_weights]
workspace = 1.0
session = 0.8
global = 0.6

[memory.gc]
max_age_days = 15

[memory.dream]
enabled = false
min_hours = 12
min_sessions = 5
stale_lock_secs = 1800
check_interval_secs = 600

[mcp_servers.fixture]
command = "fixture-mcp"
args = ["--stdio"]
env = { TOKEN = "value" }

[mcp_servers.remote]
url = "https://mcp.example/rpc"
headers = { Authorization = "Bearer token" }
bearer_token_env_var = "REMOTE_MCP_TOKEN"

[mcp_servers.legacy]
url = "https://mcp.example/sse"
type = "sse"

[lsp_servers.gopls]
command = "gopls"
transport = "stdio"
workspace_folder = "backend"
startup_timeout = 12000
shutdown_timeout = 3000
restart_on_crash = true
max_restarts = 4
extensions = [".go"]
initialization_options = { usePlaceholders = true }
settings = { gopls = { staticcheck = true } }

[toolset.web_fetch]
proxy_endpoint = "https://toml-proxy.example"
allowed_domains = ["example.com", "vercel.com/docs"]

[skills]
paths = ["~/shared-skills", "project-skills"]
ignore = ["~/shared-skills/ignored"]
disabled = ["manual-only"]

[plugins]
paths = ["~/plugins/team-tools"]
enabled = ["project-tools"]
disabled = ["old-tools"]

[ui]
keep_text_selection = "word_select"
word_separators = "./"
mouse_reporting_toggle = true
vim_mode = true
scroll_lines = 5
invert_scroll = true
prompt_suggestions = false
permission_mode = "auto"

[[permission.rules]]
action = "allow"
tool = "bash"
pattern = "git *"

[[permission.rules]]
action = "deny"
tool = "edit"
pattern = ".env*"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "provider-model-id" || cfg.BaseURL != "https://provider.example/v1" || cfg.Backend != "chat_completions" {
		t.Fatalf("unexpected model config: %#v", cfg)
	}
	if cfg.APIKey != "secret-from-env-key" {
		t.Fatalf("unexpected API key resolution: %q", cfg.APIKey)
	}
	search, enabled := cfg.WebSearchEndpoint()
	if !enabled || search.Model != "search-model-id" || search.BaseURL != "https://search.example/v1" || search.APIKey != "search-secret" {
		t.Fatalf("unexpected web search config: %#v enabled=%v", search, enabled)
	}
	if cfg.ContextWindow != 200000 || cfg.AutoCompactThresholdPercent != 80 {
		t.Fatalf("unexpected compaction config: window=%d threshold=%d", cfg.ContextWindow, cfg.AutoCompactThresholdPercent)
	}
	if !cfg.Memory.Enabled || cfg.Memory.InitialInjection || cfg.Memory.InitialInjectionMinScore == nil || *cfg.Memory.InitialInjectionMinScore != 0.25 || cfg.Memory.SaveOnEnd || !cfg.Memory.Flush.Enabled || cfg.Memory.Flush.SoftThresholdTokens != 3000 || cfg.Memory.Flush.Model != "memory-model" || cfg.Memory.Flush.MaxWriteChars != 7000 || cfg.Memory.Flush.IdleTimeoutSeconds == nil || *cfg.Memory.Flush.IdleTimeoutSeconds != 300 || cfg.Memory.Index.MaxChunkChars != 1200 || cfg.Memory.Index.ChunkOverlapChars != 200 || cfg.Memory.Search.MaxResults != 4 || cfg.Memory.Search.MinScore != 0.5 || cfg.Memory.Search.RecencyDecay != 0.9 || !cfg.Memory.Search.TemporalDecay.Enabled || cfg.Memory.Search.TemporalDecay.HalfLifeDays != 14 || !cfg.Memory.Search.MMR.Enabled || cfg.Memory.Search.MMR.Lambda != 0.5 || cfg.Memory.Search.SourceWeights["session"] != 0.8 || cfg.Memory.Search.SourceWeights["global"] != 0.6 || cfg.Memory.GC.MaxAgeDays != 15 || cfg.Memory.Dream.Enabled || cfg.Memory.Dream.MinHours != 12 || cfg.Memory.Dream.MinSessions != 5 || cfg.Memory.Dream.StaleLockSeconds != 1800 || cfg.Memory.Dream.CheckIntervalSeconds == nil || *cfg.Memory.Dream.CheckIntervalSeconds != 600 {
		t.Fatalf("unexpected memory config: %#v", cfg.Memory)
	}
	if slugs := strings.Join(cfg.ModelSlugs(), ","); slugs != "local,search" {
		t.Fatalf("model slugs=%q", slugs)
	}
	searchModel, ok := cfg.ResolveModel("search")
	if !ok || searchModel.Model != "search-model-id" || searchModel.BaseURL != "https://search.example/v1" || searchModel.Backend != "responses" || searchModel.APIKey != "search-secret" {
		t.Fatalf("search profile=%#v ok=%v", searchModel, ok)
	}
	if internal, ok := cfg.ResolveModel("search-model-id"); !ok || internal.Model != "search-model-id" {
		t.Fatalf("internal profile=%#v ok=%v", internal, ok)
	}
	if _, ok := cfg.ResolveModel("missing"); ok {
		t.Fatal("unknown model resolved")
	}
	if cfg.Pruning.KeepLastNTurns != 5 || cfg.Pruning.SoftTrimThreshold != 6000 || cfg.Pruning.SoftTrimHead != 1500 {
		t.Fatalf("unexpected pruning config: %#v", cfg.Pruning)
	}
	if cfg.MCPServers["fixture"].Command != "fixture-mcp" || cfg.MCPServers["fixture"].Env["TOKEN"] != "value" {
		t.Fatalf("unexpected MCP config: %#v", cfg.MCPServers)
	}
	if cfg.MCPServers["remote"].URL != "https://mcp.example/rpc" || cfg.MCPServers["remote"].Headers["Authorization"] != "Bearer token" || cfg.MCPServers["remote"].BearerTokenEnvVar != "REMOTE_MCP_TOKEN" {
		t.Fatalf("unexpected MCP HTTP config: %#v", cfg.MCPServers["remote"])
	}
	if cfg.MCPServers["legacy"].Type != "sse" {
		t.Fatalf("unexpected MCP SSE config: %#v", cfg.MCPServers["legacy"])
	}
	if cfg.LSPServers["gopls"].Command != "gopls" || cfg.LSPServers["gopls"].Transport != "stdio" || len(cfg.LSPServers["gopls"].Extensions) != 1 {
		t.Fatalf("unexpected LSP config: %#v", cfg.LSPServers)
	}
	if cfg.LSPServers["gopls"].InitializationOptions["usePlaceholders"] != true || cfg.LSPServers["gopls"].Settings["gopls"].(map[string]any)["staticcheck"] != true {
		t.Fatalf("unexpected LSP dynamic config: %#v", cfg.LSPServers["gopls"])
	}
	if strings.Join(cfg.Plugins.Paths, "|") != "~/plugins/team-tools" || strings.Join(cfg.Plugins.Enabled, "|") != "project-tools" || strings.Join(cfg.Plugins.Disabled, "|") != "old-tools" {
		t.Fatalf("unexpected plugin config: %#v", cfg.Plugins)
	}
	if cfg.LSPServers["gopls"].WorkspaceFolder != "backend" || cfg.LSPServers["gopls"].StartupTimeoutMS != 12000 || cfg.LSPServers["gopls"].ShutdownTimeoutMS != 3000 {
		t.Fatalf("unexpected LSP lifecycle config: %#v", cfg.LSPServers["gopls"])
	}
	if !cfg.LSPServers["gopls"].RestartOnCrash || cfg.LSPServers["gopls"].MaxRestarts == nil || *cfg.LSPServers["gopls"].MaxRestarts != 4 {
		t.Fatalf("unexpected LSP restart config: %#v", cfg.LSPServers["gopls"])
	}
	if cfg.WebFetch.ProxyEndpoint != "https://toml-proxy.example" || !cfg.WebFetch.ProxyConfigured || !cfg.WebFetch.DomainsConfigured || len(cfg.WebFetch.AllowedDomains) != 2 {
		t.Fatalf("unexpected web fetch config: %#v", cfg.WebFetch)
	}
	if len(cfg.Permission.Rules) != 2 || cfg.Permission.Rules[0].Action != "allow" || *cfg.Permission.Rules[1].Pattern != ".env*" {
		t.Fatalf("unexpected permission config: %#v", cfg.Permission)
	}
	if cfg.UI.KeepTextSelection != "word_select" || cfg.UI.WordSeparators == nil || *cfg.UI.WordSeparators != "./" || !cfg.UI.MouseReportingToggle || !cfg.UI.VimMode || cfg.UI.ScrollLines == nil || *cfg.UI.ScrollLines != 5 || !cfg.UI.InvertScroll || cfg.UI.PromptSuggestions || cfg.UI.PermissionMode != "auto" {
		t.Fatalf("unexpected UI config: %#v", cfg.UI)
	}
	if strings.Join(cfg.Skills.Paths, ",") != "~/shared-skills,project-skills" || strings.Join(cfg.Skills.Ignore, ",") != "~/shared-skills/ignored" || strings.Join(cfg.Skills.Disabled, ",") != "manual-only" {
		t.Fatalf("unexpected skills config: %#v", cfg.Skills)
	}
}

func TestFeedbackConfigPrecedence(t *testing.T) {
	t.Setenv("GROK_FEEDBACK_ENABLED", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !cfg.FeedbackEnabled {
		t.Fatalf("default config=%#v err=%v", cfg, err)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[features]\nfeedback = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.FeedbackEnabled {
		t.Fatalf("local config=%#v err=%v", cfg, err)
	}

	t.Setenv("GROK_FEEDBACK_ENABLED", "true")
	cfg, err = Load(path)
	if err != nil || !cfg.FeedbackEnabled || !cfg.feedbackEnvConfigured {
		t.Fatalf("environment config=%#v err=%v", cfg, err)
	}
	if err := applyRequirementsData(&cfg, []byte("[features]\nfeedback = false\n"), "test", false, false); err != nil || !cfg.FeedbackEnabled {
		t.Fatalf("requirements overrode environment: config=%#v err=%v", cfg, err)
	}

	t.Setenv("GROK_FEEDBACK_ENABLED", "")
	cfg = Config{FeedbackEnabled: true}
	if err := applyRequirementsData(&cfg, []byte("[features]\nfeedback = false\n"), "test", false, false); err != nil || cfg.FeedbackEnabled || !cfg.feedbackConfigured {
		t.Fatalf("requirements config=%#v err=%v", cfg, err)
	}
}

func TestVimModeDefaultsToFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.VimMode || cfg.UI.ScrollLines != nil || cfg.UI.InvertScroll {
		t.Fatalf("unexpected UI defaults: %#v", cfg.UI)
	}
}

func TestMemoryInitialInjectionMinScoreIsClamped(t *testing.T) {
	for _, test := range []struct {
		input, want float64
	}{{-1, 0}, {0.4, 0.4}, {2, 1}} {
		cfg := Config{}
		value := test.input
		applyMemoryConfig(&cfg, &fileMemoryConfig{InitialInjection: &fileMemoryInitialInjectionConfig{MinScore: &value}}, nil)
		if cfg.Memory.InitialInjectionMinScore == nil || *cfg.Memory.InitialInjectionMinScore != test.want {
			t.Errorf("input=%v min_score=%v", test.input, cfg.Memory.InitialInjectionMinScore)
		}
	}
}

func TestResolveModelPrefersExactProfileKey(t *testing.T) {
	cfg := Config{
		Model: "collision", BaseURL: "https://parent.example/v1", Backend: "responses",
		ModelProfiles: map[string]ModelProfile{
			"collision": {Model: "exact-internal", BaseURL: "https://exact.example/v1", Backend: "chat_completions"},
			"alias":     {Model: "collision", BaseURL: "https://alias.example/v1"},
		},
	}
	resolved, ok := cfg.ResolveModel("collision")
	if !ok || resolved.Model != "exact-internal" || resolved.BaseURL != "https://exact.example/v1" || resolved.Backend != "chat_completions" {
		t.Fatalf("resolved=%#v ok=%v", resolved, ok)
	}
}

func TestLoadModelCatalogFiltersAndReasoning(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GORK_MODEL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[models]
default = "smart"
allowed_models = ["fast-*", "smart", "shared-api", "entry-hidden"]
hidden_models = ["fast-hidden"]
disabled_models = ["gone"]

[model.smart]
model = "shared-api"
name = "Smart"
description = "Deep reasoning"
context_window = 200000
reasoning_effort = "high"
supports_reasoning_effort = true
reasoning_efforts = [
  "low",
  { id = "max", value = "xhigh", label = "Max", description = "Deepest", default = true },
]

[model.fast-one]
model = "fast-api"

[model.fast-hidden]
model = "hidden-api"

[model.entry-hidden]
model = "entry-hidden-api"
hidden = true

[model.gone]
model = "gone-api"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModelID != "smart" || cfg.Model != "shared-api" || cfg.ReasoningEffort != "high" || !cfg.ModelSupportsReasoningEffort {
		t.Fatalf("default model config=%#v", cfg)
	}
	id, resolved, ok := cfg.ResolveModelEntry("shared-api")
	if !ok || id != "smart" || resolved.Model != "shared-api" || len(resolved.ModelReasoningEfforts) != 2 {
		t.Fatalf("resolved id=%q config=%#v ok=%v", id, resolved, ok)
	}
	options := resolved.ModelReasoningEfforts
	if options[0].ID != "low" || options[0].Label != "Low" || options[1].ID != "max" || options[1].Value != "xhigh" || !options[1].Default {
		t.Fatalf("reasoning options=%#v", options)
	}
	if !cfg.ModelSelectable("fast-one", "fast-api") || !cfg.ModelSelectable("fast-hidden", "hidden-api") || !cfg.ModelSelectable("entry-hidden", "entry-hidden-api") || cfg.ModelSelectable("other", "other-api") {
		t.Fatalf("model filters were not applied")
	}
	if cfg.ModelVisible("fast-hidden", "hidden-api") || cfg.ModelVisible("entry-hidden", "entry-hidden-api") || !cfg.ModelVisible("fast-one", "fast-api") {
		t.Fatalf("model visibility was not applied")
	}
	if _, _, ok := cfg.ResolveModelEntry("gone"); ok {
		t.Fatal("disabled model remained resolvable")
	}
}

func TestModelWatchPathsIncludesEveryConfigLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	paths := ModelWatchPaths(path)
	for _, want := range []string{path, filepath.Join(home, "managed_config.toml"), filepath.Join(home, "requirements.toml")} {
		if !slices.Contains(paths, want) {
			t.Fatalf("paths=%#v missing %q", paths, want)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("paths are not sorted: %#v", paths)
	}
	for index := 1; index < len(paths); index++ {
		if paths[index] == paths[index-1] {
			t.Fatalf("duplicate path %q in %#v", paths[index], paths)
		}
	}
	if defaults := ModelWatchPaths(""); !slices.Contains(defaults, path) {
		t.Fatalf("default paths=%#v missing %q", defaults, path)
	}
}

func TestReloadModelCatalogUpdatesOnlyModelState(t *testing.T) {
	current := Config{
		APIKey: "live-key", BaseURL: "https://live.example", MaxSteps: 9,
		Model: "old-api", DefaultModelID: "old", defaultModelConfigured: true,
		AllowedModels: []string{"old"}, allowedModelsConfigured: true,
		ModelProfiles: map[string]ModelProfile{"old": {Model: "old-api"}},
	}
	next := Config{
		APIKey: "disk-key", BaseURL: "https://disk.example", MaxSteps: 2,
		Model: "new-api", DefaultModelID: "new", defaultModelConfigured: true,
		AllowedModels: []string{"new"}, allowedModelsConfigured: true,
		ModelProfiles: map[string]ModelProfile{"new": {Model: "new-api", ReasoningEfforts: []ReasoningEffortOption{{ID: "high"}}}},
	}
	current.ReloadModelCatalog(next)
	if current.Model != "new-api" || current.DefaultModelID != "new" || !slices.Equal(current.AllowedModels, []string{"new"}) || current.ModelProfiles["new"].Model != "new-api" {
		t.Fatalf("model catalog=%#v", current)
	}
	if current.APIKey != "live-key" || current.BaseURL != "https://live.example" || current.MaxSteps != 9 {
		t.Fatalf("live config changed=%#v", current)
	}
	next.ModelProfiles["new"] = ModelProfile{Model: "mutated"}
	if current.ModelProfiles["new"].Model != "new-api" {
		t.Fatal("model profiles were not cloned")
	}
}

func TestReloadModelCatalogClearsLocalFiltersButPreservesExternalFloor(t *testing.T) {
	local := Config{AllowedModels: []string{"old"}, allowedModelsConfigured: true}
	local.ReloadModelCatalog(Config{})
	if local.AllowedModels != nil || local.allowedModelsConfigured {
		t.Fatalf("removed local allowlist survived: %#v", local.AllowedModels)
	}

	external := Config{AllowedModels: []string{"remote-floor"}}
	external.ReloadModelCatalog(Config{Model: "updated"})
	if !slices.Equal(external.AllowedModels, []string{"remote-floor"}) {
		t.Fatalf("external allowlist was relaxed: %#v", external.AllowedModels)
	}
}

func TestReloadModelCatalogTracksExplicitModelPreference(t *testing.T) {
	current := Config{DefaultModelID: "old", defaultModelConfigured: true}
	current.ReloadModelCatalog(Config{Model: "fallback"})
	if current.HasExplicitModelPreference() || current.DefaultModelID != "" {
		t.Fatalf("removed default remained explicit: %#v", current)
	}

	current.ReloadModelCatalog(Config{Model: "new", modelConfigured: true})
	if !current.HasExplicitModelPreference() || current.Model != "new" {
		t.Fatalf("top-level preference was lost: %#v", current)
	}
}

func TestLoadRejectsInvalidModelCatalogValues(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	for _, test := range []struct {
		name, content, want string
	}{
		{name: "glob", content: "[models]\nallowed_models = [\"grok[\"]\n", want: "allowed_models"},
		{name: "effort", content: "[model.bad]\nmodel = \"bad\"\nreasoning_effort = \"ultra\"\n", want: "reasoning_effort"},
		{name: "effort option", content: "[model.bad]\nmodel = \"bad\"\nreasoning_efforts = [\"ultra\"]\n", want: "reasoning_efforts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLoadWebFetchEnvAndExplicitEmptyDomains(t *testing.T) {
	t.Setenv("GROK_WEB_FETCH_PROXY", "http://127.0.0.1:8080")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[toolset.web_fetch]\nallowed_domains = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebFetch.ProxyEndpoint != "http://127.0.0.1:8080" || !cfg.WebFetch.ProxyConfigured || !cfg.WebFetch.DomainsConfigured || len(cfg.WebFetch.AllowedDomains) != 0 {
		t.Fatalf("unexpected web fetch env config: %#v", cfg.WebFetch)
	}
}

func TestAskUserQuestionConfigPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS", "7")
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("[toolset.ask_user_question]\ntimeout_enabled = false\ntimeout_secs = 20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[toolset.ask_user_question]\ntimeout_secs = 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[toolset.ask_user_question]\ntimeout_enabled = true\ntimeout_secs = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AskUserQuestion.TimeoutEnabled || cfg.AskUserQuestion.TimeoutSeconds != 3 {
		t.Fatalf("requirements precedence=%#v", cfg.AskUserQuestion)
	}
	if err := os.Remove(filepath.Join(home, "requirements.toml")); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AskUserQuestion.TimeoutEnabled || cfg.AskUserQuestion.TimeoutSeconds != 7 {
		t.Fatalf("environment precedence=%#v", cfg.AskUserQuestion)
	}
	t.Setenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS", "0")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AskUserQuestion.TimeoutSeconds != 12 {
		t.Fatalf("file fallback=%#v", cfg.AskUserQuestion)
	}
	t.Setenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS", "18446744073709551615")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AskUserQuestion.TimeoutSeconds != 12 {
		t.Fatalf("overflow environment fallback=%#v", cfg.AskUserQuestion)
	}
	t.Setenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS", "")
	if err := os.WriteFile(path, []byte("[toolset.ask_user_question]\ntimeout_secs = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AskUserQuestion.TimeoutSeconds != 30*60 {
		t.Fatalf("zero timeout=%#v", cfg.AskUserQuestion)
	}
}

func TestAutoModeConfigDefaultsAndValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || !cfg.AutoModeEnabled() || cfg.AutoModePromptType() != "full" {
		t.Fatalf("defaults=%#v err=%v", cfg.AutoMode, err)
	}
	for _, promptType := range []string{"full", "no_user_tool_prefix", "bare_instructions", "just_command"} {
		path := filepath.Join(home, promptType+".toml")
		data := []byte("[auto_mode]\nprompt_type = \"" + promptType + "\"\nreasoning_effort = \"xhigh\"\n")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.AutoMode.PromptType != promptType || cfg.AutoMode.ReasoningEffort != "xhigh" {
			t.Fatalf("prompt_type=%q config=%#v err=%v", promptType, cfg.AutoMode, err)
		}
	}
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		path := filepath.Join(home, effort+".toml")
		if err := os.WriteFile(path, []byte("[auto_mode]\nreasoning_effort = \""+effort+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.AutoMode.ReasoningEffort != effort {
			t.Fatalf("reasoning_effort=%q config=%#v err=%v", effort, cfg.AutoMode, err)
		}
	}
	for _, data := range []string{
		"[auto_mode]\nprompt_type = \"almost_full\"\n",
		"[auto_mode]\nreasoning_effort = \"extreme\"\n",
	} {
		path := filepath.Join(home, "invalid.toml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "invalid auto_mode") {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
}

func TestAutoModeConfigLayerPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("[auto_mode]\nenabled = true\nprompt_type = \"full\"\nreasoning_effort = \"medium\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(`{"auto_mode":{"enabled":false,"prompt_type":"just_command","classifier_model":"classifier"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_AUTO_PERMISSION_MODE", "true")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[auto_mode]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoModeEnabled() || cfg.AutoMode.PromptType != "just_command" || cfg.AutoMode.ClassifierModel != "classifier" || cfg.AutoMode.ReasoningEffort != "medium" {
		t.Fatalf("effective auto mode=%#v", cfg.AutoMode)
	}
	if err := os.Remove(filepath.Join(home, "requirements.toml")); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || !cfg.AutoModeEnabled() {
		t.Fatalf("environment gate=%#v err=%v", cfg.AutoMode, err)
	}
}

func TestGoalVerifierCountConfigPrecedenceAndClamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("[goal]\nverifier_count = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nverifier_count = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_GOAL_VERIFIER_N", "1")
	cfg, err := Load(path)
	if err != nil || cfg.Goal.VerifierCount != 1 {
		t.Fatalf("environment precedence=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_VERIFIER_N", "garbage")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.VerifierCount != 4 {
		t.Fatalf("file fallback=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_VERIFIER_N", "99")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.VerifierCount != 5 {
		t.Fatalf("upper clamp=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_VERIFIER_N", "0")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.VerifierCount != 1 {
		t.Fatalf("lower clamp=%#v err=%v", cfg.Goal, err)
	}
}

func TestGoalClassifierMaxRunsConfigPrecedenceAndFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nclassifier_max_runs = 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_GOAL_CLASSIFIER_MAX", "999")
	cfg, err := Load(path)
	if err != nil || cfg.Goal.ClassifierMaxRuns != 999 {
		t.Fatalf("environment precedence=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_CLASSIFIER_MAX", "garbage")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ClassifierMaxRuns != 6 {
		t.Fatalf("file fallback=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_CLASSIFIER_MAX", "0")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ClassifierMaxRuns != 1 {
		t.Fatalf("floor=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_CLASSIFIER_MAX", "")
	if err := os.WriteFile(path, []byte("[goal]\nclassifier_max_runs = -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("negative classifier_max_runs was accepted")
	}
}

func TestGoalReverifyAfterConfigPrecedenceAndFloor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nreverify_after = 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_GOAL_REVERIFY_AFTER", "3")
	cfg, err := Load(path)
	if err != nil || cfg.Goal.ReverifyAfter != 3 {
		t.Fatalf("environment precedence=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_REVERIFY_AFTER", "garbage")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ReverifyAfter != 6 {
		t.Fatalf("file fallback=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_REVERIFY_AFTER", "0")
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ReverifyAfter != 1 {
		t.Fatalf("environment floor=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_REVERIFY_AFTER", "")
	if err := os.WriteFile(path, []byte("[goal]\nreverify_after = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ReverifyAfter != 1 {
		t.Fatalf("file floor=%#v err=%v", cfg.Goal, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Goal.ReverifyAfter != 8 {
		t.Fatalf("default=%#v err=%v", cfg.Goal, err)
	}
}

func TestGoalStrategistAndRoleModelConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	body := `[goal]
classifier_max_runs = 8
strategist_every = 3
strategist_model = { model = "strategy", agent_type = "cursor" }

[[goal.skeptic_models]]
model = "skeptic-a"
agent_type = "general-purpose"

[[goal.skeptic_models]]
agent_type = "missing-model"

[[goal.skeptic_models]]
model = "skeptic-b"
agent_type = "plan"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GoalStrategistEvery() != 3 || cfg.Goal.StrategistModel == nil || cfg.Goal.StrategistModel.Model != "strategy" || len(cfg.Goal.SkepticModels) != 2 || cfg.Goal.SkepticModels[1].Model != "skeptic-b" {
		t.Fatalf("goal config=%#v", cfg.Goal)
	}
	t.Setenv("GROK_GOAL_STRATEGIST_EVERY", "0")
	t.Setenv("GROK_GOAL_USE_CURRENT_MODEL_ONLY", "true")
	cfg, err = Load(path)
	if err != nil || cfg.GoalStrategistEvery() != 1 || !cfg.Goal.UseCurrentModelOnly {
		t.Fatalf("environment config=%#v err=%v", cfg.Goal, err)
	}

	t.Setenv("GROK_GOAL_STRATEGIST_EVERY", "")
	if err := os.WriteFile(path, []byte("[goal]\nclassifier_max_runs = 7\nstrategist_model = { agent_type = \"cursor\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.GoalStrategistEvery() != 3 || cfg.Goal.StrategistModel != nil {
		t.Fatalf("tolerant/default config=%#v err=%v", cfg.Goal, err)
	}
}

func TestGoalPlannerConfigPrecedenceAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	missing := filepath.Join(home, "missing.toml")
	cfg, err := Load(missing)
	if err != nil || cfg.GoalPlannerEnabled(false) || !cfg.GoalPlannerEnabled(true) {
		t.Fatalf("default config=%#v err=%v", cfg.Goal, err)
	}

	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nplanner_enabled = false\nplanner_model = { model = \"local-plan\", agent_type = \"plan\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.GoalPlannerEnabled(true) || cfg.Goal.PlannerModel == nil || cfg.Goal.PlannerModel.Model != "local-plan" {
		t.Fatalf("local config=%#v err=%v", cfg.Goal, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		GoalPlannerEnabled: boolPointer(true),
		GoalPlannerModel:   &GoalRoleModel{Model: "remote-plan", AgentType: "general-purpose"},
	})
	if cfg.GoalPlannerEnabled(true) || cfg.Goal.PlannerModel.Model != "local-plan" {
		t.Fatalf("remote replaced local=%#v", cfg.Goal)
	}

	t.Setenv("GROK_GOAL_PLANNER", "true")
	cfg, err = Load(path)
	if err != nil || !cfg.GoalPlannerEnabled(false) {
		t.Fatalf("environment config=%#v err=%v", cfg.Goal, err)
	}

	t.Setenv("GROK_GOAL_PLANNER", "")
	cfg, err = Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		GoalPlannerEnabled: boolPointer(false),
		GoalPlannerModel:   &GoalRoleModel{Model: "remote-plan", AgentType: "general-purpose"},
	})
	if cfg.GoalPlannerEnabled(true) || cfg.Goal.PlannerModel == nil || cfg.Goal.PlannerModel.Model != "remote-plan" {
		t.Fatalf("remote config=%#v", cfg.Goal)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalPlannerEnabled: boolPointer(true)})
	if !cfg.GoalPlannerEnabled(false) {
		t.Fatalf("remote refresh was ignored: %#v", cfg.Goal)
	}
}

func TestGoalSummaryConfigPrecedenceAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	missing := filepath.Join(home, "missing.toml")
	cfg, err := Load(missing)
	if err != nil || cfg.GoalSummaryEnabled(false) || !cfg.GoalSummaryEnabled(true) {
		t.Fatalf("default config=%#v err=%v", cfg.Goal, err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nsummary_enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.GoalSummaryEnabled(true) {
		t.Fatalf("local config=%#v err=%v", cfg.Goal, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalSummaryEnabled: boolPointer(true)})
	if cfg.GoalSummaryEnabled(true) {
		t.Fatalf("remote replaced local=%#v", cfg.Goal)
	}
	t.Setenv("GROK_GOAL_SUMMARY", "true")
	cfg, err = Load(path)
	if err != nil || !cfg.GoalSummaryEnabled(false) {
		t.Fatalf("environment config=%#v err=%v", cfg.Goal, err)
	}
	t.Setenv("GROK_GOAL_SUMMARY", "")
	cfg, err = Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalSummaryEnabled: boolPointer(false)})
	if cfg.GoalSummaryEnabled(true) {
		t.Fatalf("remote config=%#v", cfg.Goal)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalSummaryEnabled: boolPointer(true)})
	if !cfg.GoalSummaryEnabled(false) {
		t.Fatalf("remote refresh was ignored=%#v", cfg.Goal)
	}
}

func TestValidateWebFetchConfig(t *testing.T) {
	base := Config{
		APIKey: "key", BaseURL: "https://api.example/v1", Model: "model", Backend: "responses",
		MaxSteps: 1, ContextWindow: 1000, AutoCompactThresholdPercent: 85,
	}
	base.WebFetch = WebFetchConfig{ProxyEndpoint: "file:///tmp/proxy", ProxyConfigured: true}
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "proxy_endpoint") {
		t.Fatalf("unexpected proxy validation: %v", err)
	}
	base.WebFetch = WebFetchConfig{AllowedDomains: []string{"https://example.com"}, DomainsConfigured: true}
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "allowed domain") {
		t.Fatalf("unexpected domain validation: %v", err)
	}
}

func TestLoadAndValidateHashlineToolset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[toolset]\nfile_toolset = \"hashline\"\n[toolset.hashline]\nscheme = \"content_only\"\nhash_len = 2\nchunk_size = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Toolset.FileToolset != "hashline" || cfg.Toolset.Hashline.Scheme != "content_only" || cfg.Toolset.Hashline.HashLen != 2 || cfg.Toolset.Hashline.ChunkSize != 4 {
		t.Fatalf("toolset=%#v", cfg.Toolset)
	}
	if err := os.WriteFile(path, []byte("[toolset]\nfile_toolset = \"hashline\"\n[toolset.hashline]\nhash_len = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "hash_len") {
		t.Fatalf("validation error=%v", err)
	}
}

func TestHashlineToolsetDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Toolset.FileToolset != "standard" || cfg.Toolset.Hashline != (HashlineConfig{Scheme: "chunk", HashLen: 3, ChunkSize: 8}) {
		t.Fatalf("defaults=%#v", cfg.Toolset)
	}
}

func TestValidateTextSelectionMode(t *testing.T) {
	base := Config{
		APIKey: "key", BaseURL: "https://api.example/v1", Model: "model", Backend: "responses",
		MaxSteps: 1, ContextWindow: 1000, AutoCompactThresholdPercent: 85,
	}
	base.UI.KeepTextSelection = "always"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "keep_text_selection") {
		t.Fatalf("unexpected selection validation: %v", err)
	}
}

func TestLoadPreservesEmptyWordSeparators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nkeep_text_selection = \"hold\"\nword_separators = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.KeepTextSelection != "hold" || cfg.UI.WordSeparators == nil || *cfg.UI.WordSeparators != "" {
		t.Fatalf("UI config=%#v", cfg.UI)
	}
}

func TestThemeConfigCanonicalizationAndUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[ui]\ntheme = \"tokyo\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.Theme != "tokyonight" {
		t.Fatalf("theme=%q err=%v", cfg.UI.Theme, err)
	}
	if err := UpdateTheme(path, "rose-pine"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.Theme != "rosepine-moon" {
		t.Fatalf("updated theme=%q err=%v", cfg.UI.Theme, err)
	}
	if err := UpdateTheme(path, "missing"); err == nil {
		t.Fatal("unknown theme was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["models"].(map[string]any)["default"] != "local" || raw["ui"].(map[string]any)["theme"] != "rosepine-moon" {
		t.Fatalf("updated config=%#v", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
	invalidPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(invalidPath, []byte("[ui]\ntheme = \"missing\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(invalidPath); err == nil || !strings.Contains(err.Error(), "ui theme") {
		t.Fatalf("invalid configured theme error=%v", err)
	}
}

func TestLoadTextSelectionCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, config, want string
	}{
		{name: "legacy true", config: "keep_text_selection = true", want: "hold"},
		{name: "legacy false", config: "keep_text_selection = false", want: "flash"},
		{name: "legacy duration", config: "selection_highlight_duration_ms = 0", want: "hold"},
		{name: "nonzero duration", config: "selection_highlight_duration_ms = 150", want: "flash"},
		{name: "legacy word selection", config: "double_click_action = \"word_select\"", want: "word_select"},
		{name: "word selection beats duration", config: "selection_highlight_duration_ms = 0\ndouble_click_action = \"word_select\"", want: "word_select"},
		{name: "modern value wins", config: "keep_text_selection = \"flash\"\nselection_highlight_duration_ms = 0\ndouble_click_action = \"word_select\"", want: "flash"},
		{name: "invalid modern value ignores duration", config: "keep_text_selection = \"unknown\"\nselection_highlight_duration_ms = 0", want: "flash"},
		{name: "legacy word selection replaces invalid modern value", config: "keep_text_selection = \"unknown\"\ndouble_click_action = \"word_select\"", want: "word_select"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[ui]\n"+test.config+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.KeepTextSelection != test.want {
				t.Fatalf("selection=%q want=%q", cfg.UI.KeepTextSelection, test.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ui":{"keep_text_selection":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.KeepTextSelection != "hold" {
		t.Fatalf("JSON legacy selection=%q", cfg.UI.KeepTextSelection)
	}
	if err := os.WriteFile(path, []byte(`{"ui":{"keep_text_selection":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "boolean or string") {
		t.Fatalf("invalid value error=%v", err)
	}
}

func TestTextSelectionCompatibilityAcrossJSONLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("[ui]\ndouble_click_action = \"word_select\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(`{"ui":{"keep_text_selection":false,"selection_highlight_duration_ms":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.KeepTextSelection != "flash" {
		t.Fatalf("modern user value did not beat managed legacy value: %#v", cfg.UI)
	}
	if err := os.WriteFile(path, []byte(`{"ui":{"selection_highlight_duration_ms":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.KeepTextSelection != "word_select" {
		t.Fatalf("managed double-click value did not beat user duration: %#v", cfg.UI)
	}
}

func TestPromptSuggestionsConfigPrecedence(t *testing.T) {
	t.Setenv("GROK_PROMPT_SUGGESTIONS", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !cfg.UI.PromptSuggestions {
		t.Fatalf("default config=%#v err=%v", cfg.UI, err)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nprompt_suggestions = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.PromptSuggestions {
		t.Fatalf("file config=%#v err=%v", cfg.UI, err)
	}

	t.Setenv("GROK_PROMPT_SUGGESTIONS", "true")
	cfg, err = Load(path)
	if err != nil || !cfg.UI.PromptSuggestions {
		t.Fatalf("environment override=%#v err=%v", cfg.UI, err)
	}

	t.Setenv("GROK_PROMPT_SUGGESTIONS", "sometimes")
	cfg, err = Load(path)
	if err != nil || cfg.UI.PromptSuggestions {
		t.Fatalf("invalid environment value did not preserve file config=%#v err=%v", cfg.UI, err)
	}
}

func TestMouseReportingToggleEnvironmentOverridesConfig(t *testing.T) {
	t.Setenv("GROK_MOUSE_REPORTING_TOGGLE", "false")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nmouse_reporting_toggle = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.MouseReportingToggle {
		t.Fatalf("environment override=%#v", cfg.UI)
	}
	t.Setenv("GROK_MOUSE_REPORTING_TOGGLE", "sometimes")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UI.MouseReportingToggle {
		t.Fatalf("invalid environment value did not preserve config=%#v", cfg.UI)
	}
}

func TestScrollEnvironmentOverridesConfig(t *testing.T) {
	t.Setenv("GROK_SCROLL_LINES", "12")
	t.Setenv("GROK_INVERT_SCROLL", "true")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nscroll_lines = 4\ninvert_scroll = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ScrollLines == nil || *cfg.UI.ScrollLines != 10 || !cfg.UI.InvertScroll {
		t.Fatalf("environment override=%#v", cfg.UI)
	}
	t.Setenv("GROK_SCROLL_LINES", "fast")
	t.Setenv("GROK_INVERT_SCROLL", "sometimes")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ScrollLines == nil || *cfg.UI.ScrollLines != 4 || cfg.UI.InvertScroll {
		t.Fatalf("invalid environment value did not preserve config=%#v", cfg.UI)
	}
	t.Setenv("GROK_SCROLL_LINES", "0")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ScrollLines != nil {
		t.Fatalf("zero environment value did not clear override=%#v", cfg.UI)
	}
}

func TestWebSearchEndpointFallbackAndValidation(t *testing.T) {
	cfg := Config{APIKey: "key", BaseURL: "https://api.example/v1/", Model: "model", Backend: "responses"}
	search, enabled := cfg.WebSearchEndpoint()
	if !enabled || search.APIKey != "key" || search.BaseURL != "https://api.example/v1" || search.Model != "model" {
		t.Fatalf("unexpected fallback: %#v enabled=%v", search, enabled)
	}
	cfg.Backend = "anthropic_messages"
	if _, enabled := cfg.WebSearchEndpoint(); enabled {
		t.Fatal("non-Responses backend unexpectedly enabled implicit web search")
	}
	cfg.WebSearch = WebSearchConfig{Enabled: true, BaseURL: "https://search.example/v1", Model: "search"}
	if search, enabled := cfg.WebSearchEndpoint(); !enabled || search.APIKey != "key" {
		t.Fatalf("explicit search config did not inherit credentials: %#v", search)
	}
}

func TestLoadRejectsUnknownWebSearchModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\nweb_search = \"missing\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("undefined web search model was accepted")
	}
}

func TestWebSearchEnvironmentOverrides(t *testing.T) {
	t.Setenv("GORK_API_KEY", "main-key")
	t.Setenv("GORK_MODEL", "main-model")
	t.Setenv("GORK_WEB_SEARCH_API_KEY", "search-key")
	t.Setenv("GORK_WEB_SEARCH_BASE_URL", "https://search.example/v1/")
	t.Setenv("GORK_WEB_SEARCH_MODEL", "search-model")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	search, enabled := cfg.WebSearchEndpoint()
	if !enabled || search.APIKey != "search-key" || search.BaseURL != "https://search.example/v1" || search.Model != "search-model" {
		t.Fatalf("unexpected environment search config: %#v enabled=%v", search, enabled)
	}
}

func TestExternalAuthConfigurationAndEnvironment(t *testing.T) {
	t.Setenv("GROK_AUTH_PROVIDER_COMMAND", "")
	t.Setenv("GROK_AUTH_TOKEN_TTL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("auth_provider_command = \"printf file-token\"\nauth_token_ttl = 600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthProviderCommand != "printf file-token" || cfg.AuthTokenTTL != 10*time.Minute {
		t.Fatalf("file external auth config=%#v", cfg)
	}
	t.Setenv("GROK_AUTH_PROVIDER_COMMAND", "printf env-token")
	t.Setenv("GROK_AUTH_TOKEN_TTL", "120")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthProviderCommand != "printf env-token" || cfg.AuthTokenTTL != 2*time.Minute {
		t.Fatalf("environment external auth config=%#v", cfg)
	}
}

func TestTeamAuthenticationPolicy(t *testing.T) {
	t.Setenv("GROK_DISABLE_API_KEY_AUTH", "true")
	t.Setenv("GROK_OAUTH2_PRINCIPAL_TYPE", "Team")
	t.Setenv("GROK_OAUTH2_PRINCIPAL_ID", "team-env")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[grok_com_config]
force_login_team_uuid = ["team-a", "team-b"]
disable_api_key_auth = false
auth_provider_command = "printf nested-token"
auth_token_ttl = 300

[grok_com_config.oauth2]
principal_type = "Personal"
principal_id = "user-file"

[auth]
preferred_method = "oidc"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ForceLoginTeamConfigured || strings.Join(cfg.ForceLoginTeams, ",") != "team-a,team-b" || !cfg.DisableAPIKeyAuth || cfg.AuthPrincipalType != "Team" || cfg.AuthPrincipalID != "team-env" || cfg.AuthProviderCommand != "printf nested-token" || cfg.AuthTokenTTL != 5*time.Minute || cfg.PreferredAuthMethod != "oidc" {
		t.Fatalf("team auth policy=%#v", cfg)
	}
	if teams, configured, err := forceLoginTeams("team-only"); err != nil || !configured || strings.Join(teams, ",") != "team-only" {
		t.Fatalf("single team=%#v configured=%v err=%v", teams, configured, err)
	}
	if teams, configured, err := forceLoginTeams([]any{}); err != nil || !configured || teams == nil || len(teams) != 0 {
		t.Fatalf("empty team policy=%#v configured=%v err=%v", teams, configured, err)
	}
	if _, _, err := forceLoginTeams(42); err == nil {
		t.Fatal("invalid team policy was accepted")
	}
}

func TestPreferredAuthMethodValidation(t *testing.T) {
	base := Config{APIKey: "key", BaseURL: "https://api.x.ai/v1", Model: "model", Backend: "responses", MaxSteps: 1, ContextWindow: 1}
	base.PreferredAuthMethod = "invalid"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "preferred_method") {
		t.Fatalf("invalid preferred method error=%v", err)
	}
	base.PreferredAuthMethod = "api_key"
	base.DisableAPIKeyAuth = true
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting preferred method error=%v", err)
	}
}

func TestLoadJSONRemainsSupported(t *testing.T) {
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"api_key":"json-key","model":"json-model","base_url":"https://json.example/v1",
		"backend":"anthropic_messages","max_steps":7,"http_timeout":"30s"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "json-key" || cfg.Model != "json-model" || cfg.Backend != "anthropic_messages" || cfg.MaxSteps != 7 {
		t.Fatalf("unexpected JSON config: %#v", cfg)
	}
}

func TestDefaultPathMatchesGrok(t *testing.T) {
	t.Setenv("GROK_HOME", "")
	t.Setenv("HOME", t.TempDir())
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "config.toml" || filepath.Base(filepath.Dir(path)) != ".grok" {
		t.Fatalf("unexpected default path: %s", path)
	}
}

func TestDefaultPathUsesGROKHOME(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := DefaultPath()
	if err != nil || path != filepath.Join(home, "config.toml") {
		t.Fatalf("GROK_HOME config path=%q err=%v", path, err)
	}
}

func TestRequirementsOverrideUserConfigAndEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_OAUTH2_PRINCIPAL_ID", "team-env")
	t.Setenv("GROK_DISABLE_API_KEY_AUTH", "false")
	t.Setenv("GROK_AUTH_PROVIDER_COMMAND", "printf env-token")
	configPath := filepath.Join(home, "config.toml")
	configData := []byte(`
[grok_com_config]
force_login_team_uuid = "team-user"
disable_api_key_auth = false

[grok_com_config.oauth2]
principal_id = "team-user"

[auth]
preferred_method = "api_key"

[[permission.rules]]
action = "allow"
tool = "bash"
pattern = "git *"
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	requirementsData := []byte(`
fail_closed = true
[grok_com_config]
force_login_team_uuid = ["team-managed"]
disable_api_key_auth = true
auth_provider_command = ""

[grok_com_config.oauth2]
principal_id = "team-managed"

[auth]
preferred_method = "oidc"

[[permission.rules]]
action = "deny"
tool = "bash"
pattern = "git push*"
`)
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), requirementsData, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ForceLoginTeamConfigured || strings.Join(cfg.ForceLoginTeams, ",") != "team-managed" || !cfg.DisableAPIKeyAuth || cfg.AuthPrincipalID != "team-managed" || cfg.PreferredAuthMethod != "oidc" || cfg.AuthProviderCommand != "" {
		t.Fatalf("effective requirements config=%#v", cfg)
	}
	if len(cfg.Permission.Rules) != 1 || cfg.Permission.Rules[0].Action != "deny" || cfg.Permission.Rules[0].Pattern == nil || *cfg.Permission.Rules[0].Pattern != "git push*" {
		t.Fatalf("managed permission rules=%#v", cfg.Permission.Rules)
	}
}

func TestSystemRequirementsOverrideUserRequirements(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "user.toml")
	systemPath := filepath.Join(t.TempDir(), "system.toml")
	if err := os.WriteFile(userPath, []byte("[grok_com_config]\nforce_login_team_uuid = \"team-user\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemPath, []byte("[grok_com_config]\nforce_login_team_uuid = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	if err := applyRequirementsFiles(&cfg, []string{userPath, systemPath}); err != nil {
		t.Fatal(err)
	}
	if !cfg.ForceLoginTeamConfigured || cfg.ForceLoginTeams == nil || len(cfg.ForceLoginTeams) != 0 {
		t.Fatalf("system requirements did not fail closed: %#v", cfg.ForceLoginTeams)
	}
}

func TestRequirementsDisablePermissionBypass(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"new key enabled", "[ui]\ndisable_bypass_permissions_mode = true\n", true},
		{"new key disabled", "[ui]\ndisable_bypass_permissions_mode = false\n", false},
		{"legacy key disabled", "[ui]\nyolo = false\n", true},
		{"legacy key enabled", "[ui]\nyolo = true\n", false},
		{"non boolean", "[ui]\ndisable_bypass_permissions_mode = \"true\"\nyolo = 0\n", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{}
			if err := applyRequirementsData(&cfg, []byte(test.data), test.name, true, false); err != nil {
				t.Fatal(err)
			}
			if cfg.DisableBypassPermissionsMode != test.want {
				t.Fatalf("lock=%v want=%v", cfg.DisableBypassPermissionsMode, test.want)
			}
		})
	}

	cfg := Config{}
	for _, data := range []string{
		"[ui]\ndisable_bypass_permissions_mode = true\n",
		"[ui]\ndisable_bypass_permissions_mode = false\nyolo = true\n",
	} {
		if err := applyRequirementsData(&cfg, []byte(data), "layer", false, false); err != nil {
			t.Fatal(err)
		}
	}
	if !cfg.DisableBypassPermissionsMode {
		t.Fatal("later requirements layer removed the permission bypass lock")
	}
}

func TestRegularConfigCannotDisablePermissionBypass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ndisable_bypass_permissions_mode = true\nyolo = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableBypassPermissionsMode {
		t.Fatal("regular config.toml activated the managed permission lock")
	}
}

func TestRequirementsFailClosedParsing(t *testing.T) {
	t.Setenv("GROK_MANAGED_CONFIG_FAIL_CLOSED", "")
	path := filepath.Join(t.TempDir(), "requirements.toml")
	invalid := []byte("fail_closed = true\n[permission]\nrules = \"invalid\"\n")
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyRequirementsFiles(&Config{}, []string{path}); err == nil || !strings.Contains(err.Error(), "parse requirements") {
		t.Fatalf("file fail_closed error=%v", err)
	}
	if err := os.WriteFile(path, []byte("[permission]\nrules = \"invalid\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyRequirementsFiles(&Config{}, []string{path}); err != nil {
		t.Fatalf("soft requirements error=%v", err)
	}
	if err := os.WriteFile(path, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_MANAGED_CONFIG_FAIL_CLOSED", "true")
	if err := applyRequirementsFiles(&Config{}, []string{path}); err == nil || !strings.Contains(err.Error(), "parse requirements") {
		t.Fatalf("environment fail_closed error=%v", err)
	}
}

func TestRequirementsVersionOverridesRespectFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.toml")
	data := []byte("fail_closed = true\n[[version_overrides]]\nmaximum_version = \"0.1.0\"\n[version_overrides.grok_com_config]\ndisable_api_key_auth = true\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	if err := applyRequirementsFiles(&cfg, []string{path}); err != nil || !cfg.DisableAPIKeyAuth {
		t.Fatalf("matching requirements override cfg=%#v err=%v", cfg, err)
	}
	data = []byte("fail_closed = true\n[[version_overrides]]\nminimum_version = \"not-a-version\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyRequirementsFiles(&Config{}, []string{path}); err == nil || !strings.Contains(err.Error(), "minimum_version") {
		t.Fatalf("invalid fail-closed override error=%v", err)
	}
	data = []byte("[grok_com_config]\ndisable_api_key_auth = true\n[[version_overrides]]\nminimum_version = \"not-a-version\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = Config{}
	if err := applyRequirementsFiles(&cfg, []string{path}); err != nil || cfg.DisableAPIKeyAuth {
		t.Fatalf("invalid soft override layer was not skipped: cfg=%#v err=%v", cfg, err)
	}
}

func TestApplyVersionOverridesOrderingAndBounds(t *testing.T) {
	var layer map[string]any
	data := []byte(`
[feature]
value = "base"

[[version_overrides]]
minimum_version = "1.8.0"
[version_overrides.feature]
value = "late"

[[version_overrides]]
minimum_version = "1.0.0"
maximum_version = "1.8.0"
[version_overrides.feature]
value = "early"

[[version_overrides]]
minimum_version = "1.8.0"
[version_overrides.feature]
value = "same-minimum-later"
`)
	if err := toml.Unmarshal(data, &layer); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionOverrides(layer, "1.8.0"); err != nil {
		t.Fatal(err)
	}
	feature := layer["feature"].(map[string]any)
	if feature["value"] != "same-minimum-later" {
		t.Fatalf("ordered override=%#v", layer)
	}
	if _, ok := layer["version_overrides"]; ok {
		t.Fatal("version_overrides was not stripped")
	}
	reinjection := map[string]any{"version_overrides": []map[string]any{{
		"minimum_version": "1.0.0", "version_overrides": []any{}, "campaigns": []any{}, "keep": true,
	}}}
	if err := applyVersionOverrides(reinjection, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reinjection["version_overrides"]; ok {
		t.Fatal("patch reintroduced version_overrides")
	}
	if _, ok := reinjection["campaigns"]; ok {
		t.Fatal("patch reintroduced campaigns")
	}
	if reinjection["keep"] != true {
		t.Fatal("ordinary patch value was stripped")
	}
	for _, test := range []struct {
		minimum string
		maximum string
		current string
		applies bool
	}{
		{minimum: "1.7.0", current: "1.7.0", applies: true},
		{maximum: "2.0.0", current: "2.0.0", applies: true},
		{minimum: "1.7.0", current: "1.6.9"},
		{maximum: "2.0.0", current: "2.0.1"},
	} {
		entry := map[string]any{"enabled": true}
		if test.minimum != "" {
			entry["minimum_version"] = test.minimum
		}
		if test.maximum != "" {
			entry["maximum_version"] = test.maximum
		}
		candidate := map[string]any{"version_overrides": []map[string]any{entry}}
		if err := applyVersionOverrides(candidate, test.current); err != nil {
			t.Fatal(err)
		}
		_, applied := candidate["enabled"]
		if applied != test.applies {
			t.Fatalf("bounds min=%q max=%q current=%q applied=%v", test.minimum, test.maximum, test.current, applied)
		}
	}
	invalid := map[string]any{"version_overrides": []map[string]any{{"minimum_version": "v1.0.0"}}}
	if err := applyVersionOverrides(invalid, "1.0.0"); err == nil || !strings.Contains(err.Error(), "minimum_version") {
		t.Fatalf("Rust-incompatible semver was accepted: %v", err)
	}
}

func TestManagedConfigDeepMergeAndUserPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	managed := []byte(`
[models]
default = "shared"

[model.shared]
model = "managed-model"
base_url = "https://managed.example/v1"
backend = "chat_completions"
context_window = 90000

[[version_overrides]]
minimum_version = "0.1.0-dev"
[version_overrides.model.shared]
context_window = 91000

[mcp_servers.managed]
command = "managed-mcp"

[skills]
paths = ["managed-skills"]
`)
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), managed, 0o600); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(home, "config.toml")
	user := []byte(`
[model.shared]
model = "user-model"

[mcp_servers.user]
command = "user-mcp"

[skills]
paths = ["user-skills"]
`)
	if err := os.WriteFile(userPath, user, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "user-model" || cfg.BaseURL != "https://managed.example/v1" || cfg.Backend != "chat_completions" || cfg.ContextWindow != 91000 {
		t.Fatalf("merged model config=%#v", cfg)
	}
	if cfg.MCPServers["managed"].Command != "managed-mcp" || cfg.MCPServers["user"].Command != "user-mcp" {
		t.Fatalf("merged MCP config=%#v", cfg.MCPServers)
	}
	if strings.Join(cfg.Skills.Paths, ",") != "user-skills" {
		t.Fatalf("managed array was not replaced: %#v", cfg.Skills.Paths)
	}
}

func TestManagedPolicyEndpointConfiguration(t *testing.T) {
	t.Setenv("GROK_CLI_CHAT_PROXY_BASE_URL", "")
	t.Setenv("GROK_MANAGED_CONFIG_URL", "")
	t.Setenv("GROK_DEPLOYMENT_KEY", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[endpoints]
cli_chat_proxy_base_url = "https://proxy.example/v1/"
managed_config_url = "https://managed.example/config"
deployment_key = "disk-key"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyBaseURL != "https://proxy.example/v1" || cfg.ManagedPolicyURL() != "https://managed.example/config" || cfg.DeploymentKey != "disk-key" {
		t.Fatalf("managed endpoints=%#v", cfg)
	}
	t.Setenv("GROK_MANAGED_CONFIG_URL", "https://env.example/config")
	t.Setenv("GROK_DEPLOYMENT_KEY", "env-key")
	cfg, err = Load(path)
	if err != nil || cfg.ManagedPolicyURL() != "https://env.example/config" || cfg.DeploymentKey != "env-key" {
		t.Fatalf("managed endpoint env precedence=%#v err=%v", cfg, err)
	}
}

func TestManagedConfigLayerOrder(t *testing.T) {
	systemPath := filepath.Join(t.TempDir(), "system.toml")
	managedPath := filepath.Join(t.TempDir(), "managed.toml")
	userPath := filepath.Join(t.TempDir(), "user.toml")
	for path, value := range map[string]string{
		systemPath:  "[grok_com_config]\nforce_login_team_uuid = \"system\"\n",
		managedPath: "[grok_com_config]\nforce_login_team_uuid = \"managed\"\n",
		userPath:    "[grok_com_config]\nforce_login_team_uuid = \"user\"\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	disk, ok, err := loadMergedTOML([]string{systemPath, managedPath, userPath})
	if err != nil || !ok {
		t.Fatalf("load managed layers ok=%v err=%v", ok, err)
	}
	cfg := Config{}
	if err := applyFileConfig(&cfg, &disk); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ForceLoginTeams, ",") != "user" {
		t.Fatalf("layer precedence=%#v", cfg.ForceLoginTeams)
	}
}

func TestManagedConfigWithLegacyJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	managed := []byte("[[permission.rules]]\naction = \"deny\"\ntool = \"bash\"\npattern = \"danger*\"\n[mcp_servers.managed]\ncommand = \"managed-mcp\"\n")
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), managed, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	data := []byte(`{"api_key":"key","model":"json-model","base_url":"https://json.example/v1","backend":"responses","mcp_servers":{"user":{"command":"user-mcp"}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "json-model" || len(cfg.Permission.Rules) != 1 || cfg.Permission.Rules[0].Action != "deny" || cfg.MCPServers["managed"].Command != "managed-mcp" || cfg.MCPServers["user"].Command != "user-mcp" {
		t.Fatalf("managed + JSON config=%#v", cfg)
	}
}

func TestInvalidManagedConfigStopsLoading(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(home, "missing.toml")); err == nil || !strings.Contains(err.Error(), "managed_config.toml") {
		t.Fatalf("invalid managed config error=%v", err)
	}
}

func TestCompatConfigAndEnvironmentPrecedence(t *testing.T) {
	for _, name := range []string{
		"GROK_CURSOR_SKILLS_ENABLED", "GROK_CURSOR_RULES_ENABLED", "GROK_CURSOR_AGENTS_ENABLED", "GROK_CURSOR_MCPS_ENABLED", "GROK_CURSOR_HOOKS_ENABLED",
		"GROK_CLAUDE_SKILLS_ENABLED", "GROK_CLAUDE_RULES_ENABLED", "GROK_CLAUDE_AGENTS_ENABLED", "GROK_CLAUDE_MCPS_ENABLED", "GROK_CLAUDE_HOOKS_ENABLED",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("GROK_CURSOR_SKILLS_ENABLED", "yes")
	t.Setenv("GROK_CURSOR_RULES_ENABLED", "invalid")
	t.Setenv("GROK_CURSOR_MCPS_ENABLED", "on")
	t.Setenv("GROK_CURSOR_HOOKS_ENABLED", "true")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[compat.cursor]
skills = false
rules = false
agents = false
mcps = false
hooks = false

[compat.claude]
skills = false
mcps = false
hooks = false
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Compat.Cursor.Skills || cfg.Compat.Cursor.Rules || cfg.Compat.Cursor.Agents || !cfg.Compat.Cursor.Mcps || !cfg.Compat.Cursor.Hooks {
		t.Fatalf("unexpected cursor compatibility resolution: %#v", cfg.Compat.Cursor)
	}
	if cfg.Compat.Claude.Skills || !cfg.Compat.Claude.Rules || !cfg.Compat.Claude.Agents || cfg.Compat.Claude.Mcps || cfg.Compat.Claude.Hooks {
		t.Fatalf("unexpected claude compatibility resolution: %#v", cfg.Compat.Claude)
	}
}

func TestFolderTrustConfigAndEnvironmentPrecedence(t *testing.T) {
	t.Setenv("GROK_FOLDER_TRUST", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[folder_trust]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FolderTrustEnabled {
		t.Fatal("folder trust config was ignored")
	}
	t.Setenv("GROK_FOLDER_TRUST", "yes")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FolderTrustEnabled {
		t.Fatal("folder trust environment override was ignored")
	}
}

func TestUpdateSkillsPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[skills]\npaths = [\"old\"]\nignore = [\"ignored\"]\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSkills(path, func(settings *SkillsConfig) {
		settings.Paths = append(settings.Paths, "new")
		settings.Disabled = []string{"review"}
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	models := raw["models"].(map[string]any)
	skillTable := raw["skills"].(map[string]any)
	if models["default"] != "local" || strings.Join(anyStrings(skillTable["paths"]), "|") != "old|new" || strings.Join(anyStrings(skillTable["disabled"]), "|") != "review" {
		t.Fatalf("unexpected updated config: %#v", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode=%v", info.Mode().Perm())
	}
	if err := UpdateSkills(path, func(settings *SkillsConfig) { *settings = SkillsConfig{} }); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = nil
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["skills"]; exists || raw["models"].(map[string]any)["default"] != "local" {
		t.Fatalf("skills reset damaged config: %#v", raw)
	}
}

func TestUpdatePluginsPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model_name = \"grok\"\n\n[plugins]\npaths = [\"old\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdatePlugins(path, func(settings *PluginsConfig) {
		settings.Paths = append(settings.Paths, "new")
		settings.Enabled = []string{"review"}
		settings.Disabled = []string{"legacy"}
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	plugins := raw["plugins"].(map[string]any)
	if raw["model_name"] != "grok" || strings.Join(anyStrings(plugins["paths"]), "|") != "old|new" || strings.Join(anyStrings(plugins["enabled"]), "|") != "review" || strings.Join(anyStrings(plugins["disabled"]), "|") != "legacy" {
		t.Fatalf("unexpected plugins config: %#v", raw)
	}
}

func TestUIConfigPersistenceAndPermissionPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[model.local]\nmodel = \"local-api\"\n\n[ui]\nvim_mode = true\ncompact_mode = false\npermission_mode = \"ask\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdatePermissionMode(path, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVimMode(path, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateCompactMode(path, true); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimestamps(path, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimeline(path, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	remoteMode := "always-approve"
	cfg.ApplyRemoteSettings(&RemoteSettings{PermissionMode: &remoteMode})
	if cfg.UI.PermissionMode != "auto" || cfg.UI.VimMode || !cfg.UI.CompactMode || cfg.UI.ShowTimestamps || !cfg.UI.ShowTimeline || cfg.DefaultModelID != "local" || cfg.Model != "local-api" {
		t.Fatalf("config=%#v", cfg)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
	emptyPath := filepath.Join(t.TempDir(), "config.toml")
	if err := UpdateVimMode(emptyPath, true); err != nil {
		t.Fatal(err)
	}
	if err := UpdateCompactMode(emptyPath, true); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimestamps(emptyPath, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimeline(emptyPath, true); err != nil {
		t.Fatal(err)
	}
	emptyConfig, err := Load(emptyPath)
	if err != nil || !emptyConfig.UI.VimMode || !emptyConfig.UI.CompactMode || emptyConfig.UI.ShowTimestamps || !emptyConfig.UI.ShowTimeline {
		t.Fatalf("new config=%#v err=%v", emptyConfig, err)
	}

	jsonPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(jsonPath, []byte("{\"ui\":{\"vim_mode\":true,\"compact_mode\":true}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdatePermissionMode(jsonPath, "always-approve"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVimMode(jsonPath, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateCompactMode(jsonPath, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimestamps(jsonPath, false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShowTimeline(jsonPath, true); err != nil {
		t.Fatal(err)
	}
	jsonConfig, err := Load(jsonPath)
	if err != nil || jsonConfig.UI.PermissionMode != "always-approve" || jsonConfig.UI.VimMode || jsonConfig.UI.CompactMode || jsonConfig.UI.ShowTimestamps || !jsonConfig.UI.ShowTimeline {
		t.Fatalf("JSON config=%#v err=%v", jsonConfig, err)
	}

	defaultPath := filepath.Join(t.TempDir(), "default.toml")
	if err := os.WriteFile(defaultPath, []byte("[ui]\npermission_mode = \"default\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultConfig, err := Load(defaultPath)
	if err != nil || defaultConfig.UI.PermissionMode != "ask" || defaultConfig.UI.CompactMode || !defaultConfig.UI.ShowTimestamps || defaultConfig.UI.ShowTimeline {
		t.Fatalf("default config=%#v err=%v", defaultConfig, err)
	}
	remoteConfig := Config{UI: UIConfig{PermissionMode: "ask"}}
	remoteConfig.ApplyRemoteSettings(&RemoteSettings{PermissionMode: &remoteMode})
	if remoteConfig.UI.PermissionMode != "always-approve" {
		t.Fatalf("remote mode=%q", remoteConfig.UI.PermissionMode)
	}
	remoteDefault := "default"
	remoteConfig.ApplyRemoteSettings(&RemoteSettings{PermissionMode: &remoteDefault})
	if remoteConfig.UI.PermissionMode != "ask" {
		t.Fatalf("remote default mode=%q", remoteConfig.UI.PermissionMode)
	}
	invalidRemote := "deny"
	remoteConfig.ApplyRemoteSettings(&RemoteSettings{PermissionMode: &invalidRemote})
	if remoteConfig.UI.PermissionMode != "ask" {
		t.Fatalf("invalid remote mode=%q", remoteConfig.UI.PermissionMode)
	}
	if err := UpdatePermissionMode(path, "deny"); err == nil {
		t.Fatal("invalid persisted mode was accepted")
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(invalidPath, []byte("[ui]\npermission_mode = \"deny\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(invalidPath); err == nil {
		t.Fatal("invalid configured mode was accepted")
	}
}

func anyStrings(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.(string))
	}
	return result
}
