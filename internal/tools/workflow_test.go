package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workflow"
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
	tool := newWorkflowTool(func() string { return root }, &subagentHolder{})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"script_path":".grok/workflows/demo-flow.rhai","validate_only":true}`))
	if err != nil || !strings.Contains(out, `validated`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"name":"deep-research"}`))
	if err == nil || (!strings.Contains(err.Error(), "runner unavailable") && !strings.Contains(err.Error(), "not embedded")) {
		t.Fatalf("execute err=%v", err)
	}
	_ = ws
}

type captureBackend struct {
	req SubagentRequest
}

func (b *captureBackend) Description() string { return "test" }
func (b *captureBackend) Start(_ context.Context, request SubagentRequest) (SubagentResult, error) {
	b.req = request
	return SubagentResult{
		ID: "sub-1", Type: request.Type, Status: "completed",
		Output: `{"ok":true}`, DurationMS: 11, TokensUsed: 2,
	}, nil
}
func (b *captureBackend) Has(string) bool { return false }
func (b *captureBackend) Output(context.Context, string, time.Duration) (SubagentResult, error) {
	return SubagentResult{}, nil
}
func (b *captureBackend) Kill(context.Context, string) (string, error) { return "", nil }

func TestWorkflowAgentSpawnerMapsSubagent(t *testing.T) {
	backend := &captureBackend{}
	holder := &subagentHolder{}
	holder.set(backend)
	spawner := workflowAgentSpawner{holder: holder}
	result, err := spawner.SpawnAgent(context.Background(), workflow.AgentOpts{
		Prompt: "hello", Label: "lab", CapabilityMode: "read-only", IsolationWorktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.AgentID != "sub-1" {
		t.Fatalf("result=%+v", result)
	}
	if backend.req.Prompt != "hello" || backend.req.Isolation != "worktree" || backend.req.Background {
		t.Fatalf("req=%+v", backend.req)
	}
}
