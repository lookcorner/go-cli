package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestWorkflowToolValidateOnly(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
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
	path := filepath.Join(dir, "demo-flow.rhai")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newWorkflowTool(func() string { return root })
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"script_path":".grok/workflows/demo-flow.rhai","validate_only":true}`))
	if err != nil || !strings.Contains(out, `validated`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"name":"deep-research"}`))
	if err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("execute err=%v", err)
	}
	_ = ws
}
