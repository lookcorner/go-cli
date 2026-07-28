package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAndValidateNamedWorkflow(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".grok", "workflows")
	if err := os.MkdirAll(root, 0o700); err != nil {
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
	path := filepath.Join(root, "demo-flow.rhai")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pretend cwd is inside a fake git root by using dir as cwd; List uses GitRoot.
	// When GitRoot fails open to cwd, project workflows still resolve from .grok under git root.
	// For unit test, ResolvePath is enough; also ValidateScript.
	if err := ValidateScript(script); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePath(path)
	if err != nil || resolved.Name != "demo-flow" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if err := ValidateResolved(resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveByName(dir, "missing"); err == nil {
		t.Fatal("missing accepted")
	}
	builtin, err := ResolveByName(dir, "deep-research")
	if err != nil || builtin.Source != "builtin" || !strings.Contains(builtin.Script, `name: "deep-research"`) || !strings.Contains(builtin.Script, `phase("Verify")`) {
		t.Fatalf("builtin=%#v err=%v", builtin, err)
	}
	if err := ValidateResolved(builtin); err != nil {
		t.Fatal(err)
	}
}

func TestValidateScriptRejectsBadMeta(t *testing.T) {
	if err := ValidateScript("fn main() {}"); err == nil {
		t.Fatal("missing meta accepted")
	}
	bad := `let meta = #{
  name: "bad-flow",
  description: "x",
};
fn main() {
`
	if err := ValidateScript(bad); err == nil {
		t.Fatal("unbalanced accepted")
	}
}
