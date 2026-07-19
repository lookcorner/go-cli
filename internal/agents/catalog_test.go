package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestDiscoverPluginAgents(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "review.md")
	content := "---\nname: reviewer\ndescription: Review code\ntools: read_file, grep\ndisallowedTools: [shell]\nmaxTurns: 8\nmodel: fast\neffort: high\npermissionMode: plan\nisolation: worktree\nbackground: true\ninitialPrompt: Start here\nskills: [review, test]\ndiscoverSkills: false\ninheritSkills: false\nmcpInheritance:\n  named: [github, slack]\n---\n\nReview carefully.\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions, errors := DiscoverPlugins([]plugin.Plugin{{Name: "quality", AgentDirs: []string{dir}, Executable: true}})
	if len(errors) != 0 || len(definitions) != 1 {
		t.Fatalf("definitions=%#v errors=%#v", definitions, errors)
	}
	got := definitions[0]
	if got.Name != "reviewer" || got.Description != "Review code" || strings.Join(got.Tools, "|") != "read_file|grep" || strings.Join(got.DisallowedTools, "|") != "shell" || got.MaxTurns != 8 || got.Prompt != "Review carefully." || got.Model != "fast" || got.Effort != "high" || got.PermissionMode != "plan" || got.Isolation != "worktree" || got.Background == nil || !*got.Background || got.InitialPrompt != "Start here" || strings.Join(got.Skills, "|") != "review|test" || got.DiscoverSkills || got.InheritSkills || got.MCPInheritance.Mode != "named" || strings.Join(got.MCPInheritance.Names, "|") != "github|slack" {
		t.Fatalf("definition=%#v", got)
	}
}

func TestMCPInheritanceParsingAndDefaults(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, value, mode, names string
	}{
		{name: "default", mode: "all"},
		{name: "all", value: "mcpInheritance: all\n", mode: "all"},
		{name: "none", value: "mcpInheritance: none\n", mode: "none"},
		{name: "except", value: "mcpInheritance:\n  except: [internal]\n", mode: "except", names: "internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".md")
			content := "---\nname: " + test.name + "\ndescription: test\n" + test.value + "---\nPrompt\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			definition, err := Parse(path, "")
			if err != nil || definition.MCPInheritance.Mode != test.mode || strings.Join(definition.MCPInheritance.Names, "|") != test.names {
				t.Fatalf("definition=%#v err=%v", definition, err)
			}
		})
	}
}

func TestAgentHooksParseAsObject(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.md")
	content := "---\nname: valid\ndescription: test\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: ./done.sh\n---\nPrompt\n"
	if err := os.WriteFile(valid, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, err := Parse(valid, "")
	if err != nil || !strings.Contains(string(definition.Hooks), `"Stop"`) || !strings.Contains(string(definition.Hooks), `"./done.sh"`) {
		t.Fatalf("definition=%#v err=%v", definition, err)
	}
	invalid := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(invalid, []byte("---\nname: invalid\ndescription: test\nhooks: bad\n---\nPrompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(invalid, ""); err == nil || !strings.Contains(err.Error(), "hooks must be an object") {
		t.Fatalf("invalid hooks error=%v", err)
	}
}

func TestCatalogDiscoveryPrecedenceAndPluginQualification(t *testing.T) {
	home := t.TempDir()
	grokHome := filepath.Join(home, "custom-grok")
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", grokHome)
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, filepath.Join(root, ".grok", "agents", "explore.md"), "explore", "root explore")
	writeAgent(t, filepath.Join(nested, ".grok", "agents", "explore.md"), "explore", "nested explore")
	writeAgent(t, filepath.Join(grokHome, "agents", "general-purpose.md"), "general-purpose", "user override")
	writeAgent(t, filepath.Join(grokHome, "agents", "review.md"), "review", "user review")
	pluginDir := filepath.Join(root, "plugin-agents")
	writeAgent(t, filepath.Join(pluginDir, "review.md"), "review", "plugin review")

	catalog, errors := Discover(Config{
		WorkspaceRoot: nested, ProjectTrusted: true, Compat: compat.Default(),
		Plugins: []plugin.Plugin{{Name: "quality", AgentDirs: []string{pluginDir}, Executable: true}},
	})
	if len(errors) != 0 {
		t.Fatalf("errors=%#v", errors)
	}
	explore, _ := catalog.ByName("explore")
	general, _ := catalog.ByName("general-purpose")
	qualified, qualifiedOK := catalog.ByName("quality:review")
	if explore.Description != "nested explore" || explore.Scope != "project" || general.Description == "user override" || !general.Builtin || !qualifiedOK || qualified.Description != "plugin review" {
		t.Fatalf("explore=%#v general=%#v plugin=%#v", explore, general, qualified)
	}
	untrusted, _ := Discover(Config{WorkspaceRoot: nested, Compat: compat.Default()})
	if explore, _ := untrusted.ByName("explore"); !explore.Builtin {
		t.Fatalf("untrusted project agent loaded: %#v", explore)
	}
}

func writeAgent(t *testing.T, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nPrompt"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPluginAgentsSkipsNonExecutablePlugin(t *testing.T) {
	definitions, errors := DiscoverPlugins([]plugin.Plugin{{AgentDirs: []string{t.TempDir()}}})
	if len(definitions) != 0 || len(errors) != 0 {
		t.Fatalf("definitions=%#v errors=%#v", definitions, errors)
	}
}

func TestAgentFrontmatterRejectsInvalidRuntimeValues(t *testing.T) {
	for name, field := range map[string]string{
		"zero-turns": "maxTurns: 0", "bad-effort": "effort: extreme",
		"bad-permission": "permissionMode: always", "bad-isolation": "isolation: container",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.md")
			content := "---\nname: test\ndescription: test\n" + field + "\n---\nPrompt"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(path, ""); err == nil {
				t.Fatalf("accepted %s", field)
			}
		})
	}
}
