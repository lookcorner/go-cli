package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListScansBuiltinProjectAndUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(root, ".grok", "workflows")
	userDir := filepath.Join(home, "workflows")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorkflow(t, filepath.Join(projectDir, "alpha.rhai"), "alpha", "project alpha", "when project")
	writeWorkflow(t, filepath.Join(userDir, "beta.rhai"), "beta", "user beta", "")
	// User duplicate of builtin name is skipped (first-wins).
	writeWorkflow(t, filepath.Join(userDir, "deep-research.rhai"), "deep-research", "should not win", "")
	// Invalid: filename/meta mismatch.
	if err := os.WriteFile(filepath.Join(projectDir, "gamma.rhai"), []byte(`let meta = #{ name: "other", description: "nope" };`), 0o600); err != nil {
		t.Fatal(err)
	}

	listings := List(root)
	byName := map[string]Listing{}
	for _, item := range listings {
		byName[item.Name] = item
	}
	if len(byName) != 3 {
		t.Fatalf("listings=%#v", listings)
	}
	if byName["deep-research"].Source != "builtin" || byName["deep-research"].Path != nil {
		t.Fatalf("builtin=%#v", byName["deep-research"])
	}
	if byName["alpha"].Source != "project" || byName["alpha"].Description != "project alpha" || byName["alpha"].WhenToUse == nil || *byName["alpha"].WhenToUse != "when project" {
		t.Fatalf("alpha=%#v", byName["alpha"])
	}
	if byName["beta"].Source != "user" || byName["beta"].Path == nil {
		t.Fatalf("beta=%#v", byName["beta"])
	}
}

func TestListSkipsSymlinkWorkflowDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	realDir := filepath.Join(home, "real-workflows")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeWorkflow(t, filepath.Join(realDir, "hidden.rhai"), "hidden", "via symlink", "")
	if err := os.Symlink(realDir, filepath.Join(home, "workflows")); err != nil {
		t.Skip("symlink not supported")
	}
	for _, item := range List("") {
		if item.Name == "hidden" {
			t.Fatalf("symlink dir should be skipped: %#v", item)
		}
	}
}

func TestParseMetaHandlesNestedMaps(t *testing.T) {
	script := `let meta = #{
    name: "deep-research",
    description: "Research a query",
    phases: [
        #{ title: "Plan", detail: "Choose questions" },
    ],
    when_to_use: "large topics",
};
fn trimmed(s) {}
`
	meta, ok := parseMeta(script)
	if !ok || meta.name != "deep-research" || meta.description != "Research a query" || meta.whenToUse != "large topics" {
		t.Fatalf("meta=%#v ok=%v", meta, ok)
	}
}

func writeWorkflow(t *testing.T, path, name, description, whenToUse string) {
	t.Helper()
	body := "let meta = #{\n    name: \"" + name + "\",\n    description: \"" + description + "\""
	if whenToUse != "" {
		body += ",\n    when_to_use: \"" + whenToUse + "\""
	}
	body += "\n};\ncomplete(\"ok\");\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
