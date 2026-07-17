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

[mcp_servers.fixture]
command = "fixture-mcp"
args = ["--stdio"]
env = { TOKEN = "value" }

[lsp_servers.gopls]
command = "gopls"
extensions = [".go"]
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
	if cfg.MCPServers["fixture"].Command != "fixture-mcp" || cfg.MCPServers["fixture"].Env["TOKEN"] != "value" {
		t.Fatalf("unexpected MCP config: %#v", cfg.MCPServers)
	}
	if cfg.LSPServers["gopls"].Command != "gopls" || len(cfg.LSPServers["gopls"].Extensions) != 1 {
		t.Fatalf("unexpected LSP config: %#v", cfg.LSPServers)
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
