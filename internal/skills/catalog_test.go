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

func TestWorkspaceSkillsRespectGitignore(t *testing.T) {
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
	if err := catalog.scanWithIgnore(filepath.Join(root, ".gork", "skills"), "workspace:grok", root); err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 1 || names[0] != "allowed" {
		t.Fatalf("unexpected gitignore-filtered skills: %#v", names)
	}
}
