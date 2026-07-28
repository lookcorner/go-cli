package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workflow"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestWorkflowsListReturnsCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "let meta = #{\n    name: \"alpha\",\n    description: \"project alpha\",\n    when_to_use: \"tests\",\n};\n"
	if err := os.WriteFile(filepath.Join(dir, "alpha.rhai"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	server := &Server{
		output:   &output,
		sessions: map[string]*session{"sess-1": {id: "sess-1", cwd: root}},
	}
	server.handleWorkflowsList(message{
		ID:     json.RawMessage("1"),
		Method: "x.ai/workflows/list",
		Params: json.RawMessage(`{"sessionId":"sess-1"}`),
	})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	result, _ := messages[0]["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	workflows, _ := inner["workflows"].([]any)
	if len(workflows) < 2 {
		t.Fatalf("workflows=%#v full=%#v", workflows, messages[0])
	}
	names := map[string]bool{}
	for _, raw := range workflows {
		row, _ := raw.(map[string]any)
		names[row["name"].(string)] = true
	}
	if !names["deep-research"] || !names["alpha"] {
		t.Fatalf("names=%v", names)
	}

	output.Reset()
	server.handleWorkflowsList(message{
		ID:     json.RawMessage("2"),
		Method: "x.ai/workflows/list",
		Params: json.RawMessage(`{"sessionId":"missing"}`),
	})
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("error messages=%#v", messages)
	}
	result, _ = messages[0]["result"].(map[string]any)
	if result["result"] != nil || result["error"] == nil {
		t.Fatalf("unknown session response=%#v", messages[0])
	}
}

func TestNotifyWorkflowRunUsesReferenceUpdateShape(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.NotifyWorkflowRun("session-1", workflow.RunSnapshot{
		ID: "workflow-1", Revision: 3, Name: "research", Objective: "verify claims",
		Status: "completed", Phase: "Synthesize", Result: "cited report",
		StartedAt: time.Now().Add(-time.Second),
	})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["method"] != "x.ai/session_notification" {
		t.Fatalf("messages=%#v", messages)
	}
	params := messages[0]["params"].(map[string]any)
	update := params["update"].(map[string]any)
	if params["sessionId"] != "session-1" || update["sessionUpdate"] != "workflow_updated" || update["run_id"] != "workflow-1" || update["status"] != "complete" || update["current_phase"] != "Synthesize" || update["result_summary"] != "cited report" {
		t.Fatalf("params=%#v", params)
	}
	phases := update["phases"].([]any)
	if len(phases) != 1 || phases[0].(map[string]any)["state"] != "done" {
		t.Fatalf("phases=%#v", phases)
	}
}

func TestWorkflowSlashCommandExecutesThroughACP(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", t.TempDir())
	dir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "let meta = #{\n  name: \"review-changes\",\n  description: \"Review changes\",\n};\n"
	if err := os.WriteFile(filepath.Join(dir, "review-changes.rhai"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	t.Setenv("GORK_WORKFLOW_RUNNER", buildACPWorkflowRunner(t))
	runner := &agent.Runner{Tools: registry}
	name, input, ok := resolveWorkflowSlashCommand(runner, root, `/review-changes {"target":"HEAD"}`)
	if !ok || name != "review-changes" || input != `{"target":"HEAD"}` {
		t.Fatalf("name=%q input=%q ok=%v", name, input, ok)
	}

	var output bytes.Buffer
	current := &session{id: "workflow-session", ctx: context.Background(), cwd: root, runner: runner, activePrompt: -1}
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handlePromptRequest(context.Background(), message{ID: json.RawMessage("1")}, current, promptRequest{
		SessionID: current.id,
		Prompt:    []promptBlock{{Type: "text", Text: `/review-changes {"target":"HEAD"}`}},
		Meta:      map[string]any{"promptId": "workflow-prompt"},
	})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	started, completed := false, false
	for _, item := range messages {
		if item["method"] == "session/update" {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			text, _ := content["text"].(string)
			started = started || strings.Contains(text, "started in the background")
		}
		if item["method"] == "x.ai/session/prompt_complete" {
			completed = true
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	resultSeen := false
	for time.Now().Before(deadline) && !resultSeen {
		for _, run := range registry.WorkflowRuns() {
			resultSeen = run.Name == "review-changes" && run.Status == "completed" && strings.Contains(run.Result, "ACP workflow complete")
		}
		if !resultSeen {
			time.Sleep(time.Millisecond)
		}
	}
	if !started || !completed || !resultSeen || current.running {
		t.Fatalf("started=%v completed=%v result=%v running=%v messages=%#v runs=%+v", started, completed, resultSeen, current.running, messages, registry.WorkflowRuns())
	}
	status, ok := workflowManagementMessage(runner, "/workflows")
	if !ok || !strings.Contains(status, "review-changes") || !strings.Contains(status, "completed") {
		t.Fatalf("status=%q ok=%v", status, ok)
	}
	stopped, ok := workflowManagementMessage(runner, "/workflow missing stop")
	if !ok || !strings.Contains(stopped, "not running") {
		t.Fatalf("stop=%q ok=%v", stopped, ok)
	}
}

func buildACPWorkflowRunner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "runner.go")
	binary := filepath.Join(dir, "runner")
	code := `package main
import (
  "bufio"
  "encoding/json"
  "os"
)
func main() {
  scanner := bufio.NewScanner(os.Stdin)
  if !scanner.Scan() { os.Exit(2) }
  var start map[string]any
  if json.Unmarshal(scanner.Bytes(), &start) != nil || start["type"] != "start" { os.Exit(3) }
  args, ok := start["args"].(map[string]any)
  if !ok || args["target"] != "HEAD" { os.Exit(4) }
  _ = json.NewEncoder(os.Stdout).Encode(map[string]any{
    "type":"outcome", "outcome":"completed", "result":map[string]any{"report":"ACP workflow complete"},
  })
}
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workflow runner: %v\n%s", err, output)
	}
	return binary
}
