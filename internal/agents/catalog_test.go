package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestDiscoverPluginAgents(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "review.md")
	content := "---\nname: reviewer\ndescription: Review code\ntools: read_file, grep\ndisallowedTools: [shell]\nmaxTurns: 8\n---\n\nReview carefully.\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions, errors := DiscoverPlugins([]plugin.Plugin{{Name: "quality", AgentDirs: []string{dir}, Executable: true}})
	if len(errors) != 0 || len(definitions) != 1 {
		t.Fatalf("definitions=%#v errors=%#v", definitions, errors)
	}
	got := definitions[0]
	if got.Name != "reviewer" || got.Description != "Review code" || strings.Join(got.Tools, "|") != "read_file|grep" || strings.Join(got.DisallowedTools, "|") != "shell" || got.MaxTurns != 8 || got.Prompt != "Review carefully." {
		t.Fatalf("definition=%#v", got)
	}
}

func TestDiscoverPluginAgentsSkipsNonExecutablePlugin(t *testing.T) {
	definitions, errors := DiscoverPlugins([]plugin.Plugin{{AgentDirs: []string{t.TempDir()}}})
	if len(definitions) != 0 || len(errors) != 0 {
		t.Fatalf("definitions=%#v errors=%#v", definitions, errors)
	}
}
