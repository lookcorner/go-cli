package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAndValidateWorkflowSlash(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `let meta = #{
  name: "demo-flow",
  description: "demo workflow",
};
fn main() {
  complete("ok");
}
`
	if err := os.WriteFile(filepath.Join(dir, "demo-flow.rhai"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{workspace: root}
	list := m.listWorkflowsCatalog()
	if !strings.Contains(list, "deep-research") || !strings.Contains(list, "demo-flow") {
		t.Fatalf("list=%q", list)
	}
	ok := m.validateWorkflowArg("demo-flow")
	if !strings.Contains(ok, "validated") {
		t.Fatalf("validate name=%q", ok)
	}
	ok = m.validateWorkflowArg(".grok/workflows/demo-flow.rhai")
	if !strings.Contains(ok, "validated") {
		t.Fatalf("validate path=%q", ok)
	}
	if got := m.handleWorkflowSlash([]string{"run", "demo-flow"}); !strings.Contains(got, "Usage") {
		t.Fatalf("run hint=%q", got)
	}
}
