package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGrokTOMLModelAndServers(t *testing.T) {
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CUSTOM_PROVIDER_KEY", "secret-from-env-key")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[models]
default = "local"

[model.local]
model = "provider-model-id"
base_url = "https://provider.example/v1"
backend = "chat_completions"
env_key = ["MISSING_KEY", "CUSTOM_PROVIDER_KEY"]
context_window = 200000
auto_compact_threshold_percent = 80

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

[lsp_servers.gopls]
command = "gopls"
extensions = [".go"]

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
	if cfg.LSPServers["gopls"].Command != "gopls" || len(cfg.LSPServers["gopls"].Extensions) != 1 {
		t.Fatalf("unexpected LSP config: %#v", cfg.LSPServers)
	}
	if len(cfg.Permission.Rules) != 2 || cfg.Permission.Rules[0].Action != "allow" || *cfg.Permission.Rules[1].Pattern != ".env*" {
		t.Fatalf("unexpected permission config: %#v", cfg.Permission)
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
	t.Setenv("HOME", t.TempDir())
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "config.toml" || filepath.Base(filepath.Dir(path)) != ".grok" {
		t.Fatalf("unexpected default path: %s", path)
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
