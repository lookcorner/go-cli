package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
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

func TestDeepResearchSlashUsesWorkflowTool(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	t.Setenv("GORK_WORKFLOW_RUNNER", buildTUIWorkflowRunner(t))

	m := &model{ctx: context.Background(), runner: &agent.Runner{Tools: registry}}
	m.setInput("/deep-research verify widgets")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.status != "deep research running" || !strings.Contains(m.transcript.String(), "Deep research started") {
		t.Fatalf("status=%q command=%v transcript=%q", m.status, command != nil, m.transcript.String())
	}
	updated, followup := m.Update(command())
	m = updated.(*model)
	if followup != nil || m.status != "deep research complete" || !strings.Contains(m.transcript.String(), "verified report") {
		t.Fatalf("status=%q followup=%v transcript=%q", m.status, followup != nil, m.transcript.String())
	}
}

func TestNamedWorkflowSlashPreservesJSONObjectArgs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `let meta = #{
  name: "demo-flow",
  description: "demo workflow",
};
fn main() { complete("ok"); }
`
	if err := os.WriteFile(filepath.Join(dir, "demo-flow.rhai"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	t.Setenv("GORK_WORKFLOW_RUNNER", buildTUIWorkflowRunner(t))

	m := &model{ctx: context.Background(), workspace: root, runner: &agent.Runner{Tools: registry}}
	m.setInput(`/workflow demo-flow {"target":"origin/main...HEAD","depth":3}`)
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.status != "workflow running" || !strings.Contains(m.transcript.String(), "demo-flow") {
		t.Fatalf("status=%q command=%v transcript=%q", m.status, command != nil, m.transcript.String())
	}
	updated, followup := m.Update(command())
	m = updated.(*model)
	if followup != nil || m.status != "workflow complete" || !strings.Contains(m.transcript.String(), "verified report") {
		t.Fatalf("status=%q followup=%v transcript=%q", m.status, followup != nil, m.transcript.String())
	}
}

func TestNamedWorkflowArgsMatchReferenceFallback(t *testing.T) {
	if namedWorkflowArgs("") != nil {
		t.Fatal("empty input did not preserve null args")
	}
	var args map[string]any
	if err := json.Unmarshal(namedWorkflowArgs("review release"), &args); err != nil || args["query"] != "review release" || args["objective"] != "review release" {
		t.Fatalf("args=%#v err=%v", args, err)
	}
	if err := json.Unmarshal(namedWorkflowArgs(`{"depth":3}`), &args); err != nil || args["depth"] != float64(3) {
		t.Fatalf("typed args=%#v err=%v", args, err)
	}
}

func TestWorkflowLaunchArgsKeepManagementFormsSeparate(t *testing.T) {
	for _, fields := range [][]string{nil, {"validate", "demo"}, {"pause", "demo"}, {"demo", "stop"}} {
		if name, input, ok := workflowLaunchArgs(fields); ok {
			t.Fatalf("fields=%v launched name=%q input=%q", fields, name, input)
		}
	}
	name, input, ok := workflowLaunchArgs([]string{"demo", `{"depth":3}`})
	if !ok || name != "demo" || input != `{"depth":3}` {
		t.Fatalf("name=%q input=%q ok=%v", name, input, ok)
	}
}

func buildTUIWorkflowRunner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "runner.go")
	binary := filepath.Join(dir, "runner")
	code := `package main
import (
  "bufio"
  "encoding/json"
  "os"
  "strings"
)
func main() {
  scanner := bufio.NewScanner(os.Stdin)
  if !scanner.Scan() { os.Exit(2) }
  var start map[string]any
  if json.Unmarshal(scanner.Bytes(), &start) != nil || start["type"] != "start" { os.Exit(3) }
  script, _ := start["script"].(string)
  args, _ := start["args"].(map[string]any)
	if strings.Contains(script, "name: \"deep-research\"") {
		if args["query"] != "verify widgets" { os.Exit(4) }
	} else if strings.Contains(script, "name: \"demo-flow\"") {
		if args["target"] != "origin/main...HEAD" || args["depth"] != float64(3) { os.Exit(4) }
	} else { os.Exit(4) }
  _ = json.NewEncoder(os.Stdout).Encode(map[string]any{
    "type":"outcome", "outcome":"completed", "result":map[string]any{"report":"verified report"},
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

func TestDeepResearchRunnerUnavailable(t *testing.T) {
	message := runDeepResearch(context.Background(), nil, "query")()
	done, ok := message.(workflowDoneEvent)
	if !ok || done.err == nil || !strings.Contains(done.err.Error(), "unavailable") {
		t.Fatalf("message=%#v", message)
	}
}

func TestDeepResearchSlashRequiresQuery(t *testing.T) {
	m := &model{}
	m.setInput("/deep-research")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "deep research query required" || !strings.Contains(m.transcript.String(), "Usage: /deep-research <query>") {
		t.Fatalf("status=%q command=%v transcript=%q", m.status, command != nil, m.transcript.String())
	}
}
