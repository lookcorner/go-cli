package claudeimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/pelletier/go-toml/v2"
)

func TestScanAndApplyIsSelectiveRedactedAndIdempotent(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"allow":["Bash(go test *)","WebSearch"]},"env":{"TOKEN":"global-secret"},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"check","timeout":5},{"type":"http","url":"x"}]}]}}`)
	writeFixture(t, filepath.Join(root, ".claude", "settings.local.json"), `{"env":{"TOKEN":"project-secret"}}`)
	writeFixture(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"global":{"command":"global"}}}`)
	writeFixture(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"project":{"command":"project"}}}`)
	writeFixture(t, filepath.Join(home, ".claude", "skills", "review", "SKILL.md"), "skill")

	plan := scan(root, home)
	if len(plan.Items) != 7 {
		t.Fatalf("items=%d %#v", len(plan.Items), plan.Items)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "WebSearch") {
		t.Fatalf("warnings=%#v", plan.Warnings)
	}
	for _, item := range plan.Items {
		if strings.Contains(item.Label(), "secret") {
			t.Fatalf("secret leaked in %q", item.Label())
		}
	}
	selected := map[string]bool{}
	for _, item := range plan.Items {
		selected[item.ID] = item.Scope == Global
	}
	t.Setenv("GROK_HOME", filepath.Join(home, "native"))
	first, err := Apply(plan, selected)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != 5 {
		t.Fatalf("imported=%d files=%#v", first.Imported, first.ModifiedFiles)
	}
	second, err := Apply(plan, selected)
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 {
		t.Fatalf("second import=%d", second.Imported)
	}

	data, err := os.ReadFile(filepath.Join(home, "native", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Env        map[string]string                 `toml:"env"`
		Permission config.PermissionConfig           `toml:"permission"`
		MCP        map[string]config.MCPServerConfig `toml:"mcp_servers"`
		Compat     struct {
			Claude struct {
				Skills, Mcps, Hooks bool
				Rules, Agents       *bool
			} `toml:"claude"`
		} `toml:"compat"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Env["TOKEN"] != "global-secret" || cfg.MCP["global"].Command != "global" || len(cfg.Permission.Rules) != 1 {
		t.Fatalf("config=%#v", cfg)
	}
	if cfg.Compat.Claude.Skills || cfg.Compat.Claude.Mcps || cfg.Compat.Claude.Hooks || cfg.Compat.Claude.Rules != nil || cfg.Compat.Claude.Agents != nil {
		t.Fatalf("compat=%#v", cfg.Compat.Claude)
	}
	if data, err := os.ReadFile(filepath.Join(home, "native", "skills", "review", "SKILL.md")); err != nil || string(data) != "skill" {
		t.Fatalf("skill=%q err=%v", data, err)
	}
}

func TestApplyRejectsInvalidExistingConfigWithoutDamage(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, ".grok", "config.toml")
	original := []byte("[broken\n")
	writeFixture(t, target, string(original))
	plan := Plan{Home: home, ProjectRoot: t.TempDir()}
	if _, err := Apply(plan, nil); err == nil {
		t.Fatal("expected invalid TOML error")
	}
	data, _ := os.ReadFile(target)
	if string(data) != string(original) {
		t.Fatalf("config changed: %q", data)
	}
}

func TestEmptyImportWritesOnlyCompatibilityCutoff(t *testing.T) {
	home := t.TempDir()
	plan := Plan{Home: home, ProjectRoot: t.TempDir()}
	if _, err := Apply(plan, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".grok", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "[env]") || strings.Contains(text, "[permission]") || strings.Contains(text, "[mcp_servers]") || !strings.Contains(text, "[compat.claude]") {
		t.Fatalf("config=%q", text)
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
