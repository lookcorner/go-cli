package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

[mcp_servers.fixture]
command = "fixture-mcp"
args = ["--stdio"]
env = { TOKEN = "value" }

[mcp_servers.remote]
url = "https://mcp.example/rpc"
headers = { Authorization = "Bearer token" }

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
	if cfg.Pruning.KeepLastNTurns != 5 || cfg.Pruning.SoftTrimThreshold != 6000 || cfg.Pruning.SoftTrimHead != 1500 {
		t.Fatalf("unexpected pruning config: %#v", cfg.Pruning)
	}
	if cfg.MCPServers["fixture"].Command != "fixture-mcp" || cfg.MCPServers["fixture"].Env["TOKEN"] != "value" {
		t.Fatalf("unexpected MCP config: %#v", cfg.MCPServers)
	}
	if cfg.MCPServers["remote"].URL != "https://mcp.example/rpc" || cfg.MCPServers["remote"].Headers["Authorization"] != "Bearer token" {
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
	if strings.Join(cfg.Skills.Paths, ",") != "~/shared-skills,project-skills" || strings.Join(cfg.Skills.Ignore, ",") != "~/shared-skills/ignored" || strings.Join(cfg.Skills.Disabled, ",") != "manual-only" {
		t.Fatalf("unexpected skills config: %#v", cfg.Skills)
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
	data := []byte("fail_closed = true\n[[version_overrides]]\nminimum_version = \"1.0.0\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyRequirementsFiles(&Config{}, []string{path}); err == nil || !strings.Contains(err.Error(), "version_overrides") {
		t.Fatalf("unsupported fail-closed version override error=%v", err)
	}
	data = []byte("[grok_com_config]\ndisable_api_key_auth = true\n[[version_overrides]]\nminimum_version = \"1.0.0\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	if err := applyRequirementsFiles(&cfg, []string{path}); err != nil || cfg.DisableAPIKeyAuth {
		t.Fatalf("soft version override layer was not skipped: cfg=%#v err=%v", cfg, err)
	}
}

func TestCompatConfigAndEnvironmentPrecedence(t *testing.T) {
	for _, name := range []string{
		"GROK_CURSOR_SKILLS_ENABLED", "GROK_CURSOR_RULES_ENABLED", "GROK_CURSOR_AGENTS_ENABLED",
		"GROK_CLAUDE_SKILLS_ENABLED", "GROK_CLAUDE_RULES_ENABLED", "GROK_CLAUDE_AGENTS_ENABLED",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("GROK_CURSOR_SKILLS_ENABLED", "yes")
	t.Setenv("GROK_CURSOR_RULES_ENABLED", "invalid")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[compat.cursor]
skills = false
rules = false
agents = false

[compat.claude]
skills = false
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Compat.Cursor.Skills || cfg.Compat.Cursor.Rules || cfg.Compat.Cursor.Agents {
		t.Fatalf("unexpected cursor compatibility resolution: %#v", cfg.Compat.Cursor)
	}
	if cfg.Compat.Claude.Skills || !cfg.Compat.Claude.Rules || !cfg.Compat.Claude.Agents {
		t.Fatalf("unexpected claude compatibility resolution: %#v", cfg.Compat.Claude)
	}
}
