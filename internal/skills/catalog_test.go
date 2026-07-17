package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogScanAndTool(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: code-review\ndescription: Review a code change\n---\n# Steps\nInspect the diff.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
	if err := catalog.scan(root, "test"); err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 1 || names[0] != "code-review" {
		t.Fatalf("unexpected names: %#v", names)
	}
	if !strings.Contains(catalog.Summary(), "Review a code change") {
		t.Fatalf("unexpected summary: %s", catalog.Summary())
	}
	output, err := catalog.Tool().Execute(context.Background(), json.RawMessage(`{"name":"code-review"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Inspect the diff") || !strings.Contains(output, "Source: test") {
		t.Fatalf("unexpected skill output: %s", output)
	}
}

func TestWorkspaceSkillOverridesUserSkill(t *testing.T) {
	userRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	for root, body := range map[string]string{userRoot: "user", workspaceRoot: "workspace"} {
		dir := filepath.Join(root, "same")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: same\n---\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
	if err := catalog.scan(userRoot, "user"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.scan(workspaceRoot, "workspace"); err != nil {
		t.Fatal(err)
	}
	if catalog.byName["same"].Source != "workspace" {
		t.Fatalf("workspace skill did not override: %#v", catalog.byName["same"])
	}
}

func TestWorkspaceSkillsLoadWhenGitignored(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for name := range map[string]bool{"allowed": true, "ignored": true} {
		dir := filepath.Join(root, ".gork", "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".gork/skills/ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
	if err := catalog.scan(filepath.Join(root, ".gork", "skills"), "workspace:grok"); err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 2 || names[0] != "allowed" || names[1] != "ignored" {
		t.Fatalf("gitignored skill directories should still load: %#v", names)
	}
}

func TestDiscoverScopesSkillsFromHomeThroughWorkspace(t *testing.T) {
	home := t.TempDir()
	grokHome := filepath.Join(home, "custom-grok-home")
	repo := filepath.Join(t.TempDir(), "repo")
	cwd := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	writeSkill := func(root, name, body string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + body + "\n---\n" + body
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(filepath.Join(grokHome, "skills"), "shared", "user")
	writeSkill(filepath.Join(repo, ".grok", "skills"), "shared", "repo")
	writeSkill(filepath.Join(repo, "services", ".agents", "skills"), "shared", "service")
	writeSkill(filepath.Join(cwd, ".claude", "skills"), "shared", "cwd")
	writeSkill(filepath.Join(home, ".cursor", "skills"), "cursor-only", "cursor")

	catalog, err := discover(cwd, home, grokHome)
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.byName["shared"]; got.Description != "cwd" || got.Source != "workspace:claude" {
		t.Fatalf("deepest skill should win: %#v", got)
	}
	if got := catalog.byName["cursor-only"]; got.Source != "user:cursor" {
		t.Fatalf("cursor home skill not discovered: %#v", got)
	}
}
