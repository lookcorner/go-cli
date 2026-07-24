package inspect

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/version"
)

func TestBuildDiscoversWorkspaceAndRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	home := t.TempDir()
	grokHome := filepath.Join(home, ".grok")
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", grokHome)
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	pluginRoot := filepath.Join(root, "project-plugin")
	marketplaceRoot := filepath.Join(root, "marketplace")
	writeInspectFile(t, filepath.Join(root, "AGENTS.md"), "Project instructions")
	writeInspectFile(t, filepath.Join(root, ".grok", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review code\nuser-invocable: true\n---\nReview.\n")
	writeInspectFile(t, filepath.Join(root, ".grok", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: Review changes\n---\nReview carefully.\n")
	writeInspectFile(t, filepath.Join(root, ".grok", "hooks", "checks.json"), `{"hooks":{"PreToolUse":[{"matcher":"shell","hooks":[{"type":"command","command":"SUPER_SECRET"}]}]}}`)
	writeInspectFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"local":{"command":"SUPER_SECRET","env":{"TOKEN":"SUPER_SECRET"}}}}`)
	writeInspectFile(t, filepath.Join(root, ".grok", "lsp.json"), `{"gopls":{"command":"/private/bin/gopls","extensions":[".go"]}}`)
	writeInspectFile(t, filepath.Join(pluginRoot, "plugin.json"), `{"name":"quality","skills":"skills"}`)
	writeInspectFile(t, filepath.Join(pluginRoot, "skills", "quality", "SKILL.md"), "---\nname: quality\ndescription: Quality checks\n---\nCheck.\n")
	writeInspectFile(t, filepath.Join(marketplaceRoot, ".grok-plugin", "marketplace.json"), `{"name":"local"}`)
	configPath := filepath.Join(grokHome, "config.toml")
	writeInspectFile(t, configPath, `
[plugins]
paths = ["`+pluginRoot+`"]
enabled = ["quality"]

[[marketplace.sources]]
name = "Local catalog"
path = "`+marketplaceRoot+`"

[[marketplace.sources]]
name = "Private catalog"
git = "https://SUPER_SECRET@example.com/catalog.git?token=SUPER_SECRET#main"

[ui]
permission_mode = "auto"

[[permission.rules]]
action = "allow"
tool = "read"

[subagents.toggle]
explore = false
`)

	report, err := Build(root, configPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.CWD != canonicalRoot || report.ProjectRoot != canonicalRoot || !report.ProjectTrusted {
		t.Fatalf("workspace identity=%#v", report)
	}
	if report.Permissions.Mode != "auto" || report.Permissions.Rules != 1 {
		t.Fatalf("permissions=%#v", report.Permissions)
	}
	if agentEnabled(report, "explore") {
		t.Fatalf("disabled agent reported enabled: %#v", report.Agents)
	}
	for label, found := range map[string]bool{
		"instructions": hasInstruction(report, "AGENTS.md"),
		"hooks":        hasHook(report, "checks"),
		"skills":       hasSkill(report, "review"),
		"agents":       hasAgent(report, "reviewer"),
		"plugins":      hasPlugin(report, "quality"),
		"marketplaces": hasMarketplace(report, "Local catalog"),
		"private":      hasMarketplace(report, "Private catalog"),
		"mcp":          hasMCP(report, "local"),
		"lsp":          hasLSP(report, "gopls"),
		"config":       hasConfigSource(report, configPath),
	} {
		if !found {
			t.Fatalf("%s missing from report: %#v", label, report)
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SUPER_SECRET") || strings.Contains(string(data), "/private/bin/gopls") {
		t.Fatalf("report leaked sensitive target data: %s", data)
	}
	if !strings.Contains(string(data), "https://example.com/catalog.git") {
		t.Fatalf("report removed the marketplace identity: %s", data)
	}
}

func TestBuildGatesUntrustedProjectExecution(t *testing.T) {
	currentVersion := version.Current
	version.Current = "0.1.0"
	t.Cleanup(func() { version.Current = currentVersion })
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	writeInspectFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"project":{"command":"project"}}}`)

	report, err := Build(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectTrusted || hasMCP(report, "project") {
		t.Fatalf("untrusted project execution was exposed: %#v", report)
	}
}

func writeInspectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasInstruction(report Report, name string) bool {
	for _, item := range report.Instructions {
		if strings.EqualFold(filepath.Base(item.Path), name) {
			return true
		}
	}
	return false
}

func hasHook(report Report, name string) bool {
	for _, item := range report.Hooks {
		if strings.Contains(item.Name, name) {
			return true
		}
	}
	return false
}

func hasSkill(report Report, name string) bool {
	for _, item := range report.Skills {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasAgent(report Report, name string) bool {
	for _, item := range report.Agents {
		if item.Name == name {
			return true
		}
	}
	return false
}

func agentEnabled(report Report, name string) bool {
	for _, item := range report.Agents {
		if item.Name == name {
			return item.Enabled
		}
	}
	return false
}

func hasPlugin(report Report, name string) bool {
	for _, item := range report.Plugins {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasMarketplace(report Report, name string) bool {
	for _, item := range report.Marketplaces {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasMCP(report Report, name string) bool {
	for _, item := range report.MCPServers {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasLSP(report Report, name string) bool {
	for _, item := range report.LSPServers {
		if item.Name == name {
			return true
		}
	}
	return false
}

func hasConfigSource(report Report, path string) bool {
	for _, item := range report.ConfigSources {
		if item.Path == path {
			return true
		}
	}
	return false
}
