package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/compat"
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

func TestParseMetadataNormalizesNames(t *testing.T) {
	for input, want := range map[string]string{
		"narrate_crash_video": "narrate-crash-video",
		"tool-v1.2":           "tool-v1-2",
		" spaced  name ":      "spaced-name",
	} {
		name, _, _ := parseMetadata("---\nname: "+input+"\n---\n", "fallback")
		if name != want {
			t.Errorf("parseMetadata name %q = %q, want %q", input, name, want)
		}
	}
	name, description, _ := parseMetadata("---\nname: 日本語\ndescription: kept\n---\n", "valid.dir")
	if name != "valid-dir" || description != "kept" {
		t.Fatalf("invalid name did not use normalized directory fallback: name=%q description=%q", name, description)
	}
}

func TestParseMetadataPaths(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{"scalar", "src/**, docs", []string{"src", "docs"}},
		{"space", `"my dir/**"`, []string{"my dir"}},
		{"braces", "a/{b,c}/{d,e}, docs", []string{"a/{b,c}/{d,e}", "docs"}},
		{"list", "[src/**, docs]", []string{"src", "docs"}},
		{"match all", `"**"`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, got := parseMetadata("---\nname: x\npaths: "+test.value+"\n---\n", "x")
			if strings.Join(got, "|") != strings.Join(test.want, "|") {
				t.Fatalf("paths = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseMetadataDescriptionFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"prose before heading", "Does a real thing.\n\n# Title\n", "Does a real thing."},
		{"prose after heading", "# Title\n\nDoes a real thing.\n", "Does a real thing."},
		{"heading only", "# Only A Title\n", "Only A Title"},
		{"skip structure", "![CI](badge.svg)\n\n> Old.\n\n- metadata\n\nFormats staged files.\n", "Formats staged files."},
		{"name fallback", "| A | B |\n|---|---|\n", "x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, got, _ := parseMetadata("---\nname: x\n---\n\n"+test.body, "x")
			if got != test.want {
				t.Fatalf("description = %q, want %q", got, test.want)
			}
		})
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

	catalog, err := discover(cwd, home, grokHome, compat.Default())
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

func TestSkillCompatibilityGatesAreIndependent(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	for _, file := range []struct {
		root string
		name string
	}{
		{filepath.Join(home, ".cursor", "skills"), "cursor-home"},
		{filepath.Join(home, ".claude", "skills"), "claude-home"},
		{filepath.Join(repo, ".cursor", "skills"), "cursor-project"},
		{filepath.Join(repo, ".claude", "skills"), "claude-project"},
	} {
		dir := filepath.Join(file.root, file.name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+file.name+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := compat.Default()
	cfg.Cursor.Skills = false
	catalog, err := discover(repo, home, filepath.Join(home, ".grok"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(catalog.Names(), ",")
	if strings.Contains(joined, "cursor-") || !strings.Contains(joined, "claude-home") || !strings.Contains(joined, "claude-project") {
		t.Fatalf("unexpected gated skills: %s", joined)
	}
}

func TestConditionalSkillActivatesForMatchingToolPath(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "go-files")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: go-files\ndescription: Go guidance\npaths:\n  - 'src/{main,lib}.go'\n  - 'src/generated/**'\n  - '!src/generated/**'\n---\nUse Go guidance.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{root: root, byName: make(map[string]Skill), pending: make(map[string]Skill)}
	if err := catalog.scan(root, "test"); err != nil {
		t.Fatal(err)
	}
	if names := catalog.Names(); len(names) != 0 || catalog.Count() != 1 {
		t.Fatalf("conditional skill should start hidden: names=%#v count=%d", names, catalog.Count())
	}
	if reminder := catalog.Activate("grep", json.RawMessage(`{"path":"src/main.go"}`)); reminder != "" {
		t.Fatalf("grep must not activate conditional skills: %s", reminder)
	}
	if reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"src/other.go"}`)); reminder != "" {
		t.Fatalf("nonmatching path activated skill: %s", reminder)
	}
	if reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"src/generated/file.go"}`)); reminder != "" {
		t.Fatalf("negated path activated skill: %s", reminder)
	}
	reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"src/main.go"}`))
	if !strings.Contains(reminder, "go-files") || len(catalog.Names()) != 1 {
		t.Fatalf("matching path did not activate skill: reminder=%q names=%#v", reminder, catalog.Names())
	}
}

func TestMatchesPathsChecksParentDirectories(t *testing.T) {
	if !matchesPaths([]string{"src"}, "src/pkg/main.go") {
		t.Fatal("directory pattern should match a file below that directory")
	}
	if !matchesPaths([]string{"**/*.go"}, "main.go") {
		t.Fatal("doublestar pattern should match a root-level file")
	}
	if matchesPaths([]string{"src/**", "!src/generated/**"}, "src/generated/file.go") {
		t.Fatal("later negation should exclude the generated path")
	}
}

func TestCatalogDiscoversSkillsBelowInitialWorkspace(t *testing.T) {
	root := t.TempDir()
	catalog, err := discover(root, "", "", compat.Default())
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "service", ".grok", "skills", "service-review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: service-review\ndescription: Review service code\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"service/main.go"}`))
	if !strings.Contains(reminder, "service-review") || catalog.byName["service-review"].Source != "workspace:grok" {
		t.Fatalf("nested skill was not discovered: reminder=%q catalog=%#v", reminder, catalog.byName)
	}
}

func TestCatalogRegistersDirectlyTouchedSkillFile(t *testing.T) {
	root := t.TempDir()
	existingDir := filepath.Join(root, ".grok", "skills", "existing")
	if err := os.MkdirAll(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "SKILL.md"), []byte("---\nname: existing\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := discover(root, "", "", compat.Default())
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, ".grok", "skills", "new-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("---\nname: new-skill\ndescription: Added now\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reminder := catalog.Activate("write_file", json.RawMessage(`{"path":".grok/skills/new-skill/SKILL.md"}`))
	if !strings.Contains(reminder, "new-skill") {
		t.Fatalf("directly touched skill was not registered: %q", reminder)
	}
}

func TestDynamicallyDiscoveredConditionalSkillWaitsForNextTouch(t *testing.T) {
	root := t.TempDir()
	catalog, err := discover(root, "", "", compat.Default())
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "service", ".agents", "skills", "go-only")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: go-only\npaths: ['service/**']\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"service/first.go"}`)); reminder != "" {
		t.Fatalf("new conditional skill activated on its discovery touch: %q", reminder)
	}
	if reminder := catalog.Activate("read_file", json.RawMessage(`{"path":"service/second.go"}`)); !strings.Contains(reminder, "go-only") {
		t.Fatalf("conditional skill did not activate on the next touch: %q", reminder)
	}
}

func TestCatalogWatcherReloadsAddedChangedAndDeletedSkills(t *testing.T) {
	root := t.TempDir()
	catalog, err := discover(root, "", "", compat.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	catalog.Watch(ctx, 5*time.Millisecond)
	skillDir := filepath.Join(root, ".grok", "skills", "watched")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: watched\ndescription: first\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForSkillNames(t, catalog, "watched")
	if reminder := catalog.DrainReminder(); !strings.Contains(reminder, "first") {
		t.Fatalf("missing added skill reminder: %q", reminder)
	}
	if err := os.WriteFile(path, []byte("---\nname: watched\ndescription: first\n---\nchanged body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if reminder := catalog.DrainReminder(); strings.Contains(reminder, "first") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("modified skill was not reloaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitForSkillNames(t, catalog)
	if reminder := catalog.DrainReminder(); !strings.Contains(reminder, "Skills changed on disk") {
		t.Fatalf("missing deleted skill reminder: %q", reminder)
	}
}

func waitForSkillNames(t *testing.T, catalog *Catalog, want ...string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if strings.Join(catalog.Names(), ",") == strings.Join(want, ",") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("skill names=%#v, want %#v", catalog.Names(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
