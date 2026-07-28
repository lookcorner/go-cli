package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workflow"
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
	if followup != nil || m.status != "deep research started" && m.status != "deep research completed" || !strings.Contains(m.transcript.String(), "started in the background") {
		t.Fatalf("status=%q followup=%v transcript=%q", m.status, followup != nil, m.transcript.String())
	}
	wantTUIWorkflowRun(t, registry, "deep-research", "completed")
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

	for _, input := range []string{
		`/workflow demo-flow {"target":"origin/main...HEAD","depth":3}`,
		`/demo-flow {"target":"origin/main...HEAD","depth":3}`,
	} {
		m := &model{ctx: context.Background(), workspace: root, runner: &agent.Runner{Tools: registry}}
		m.setInput(input)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command == nil || m.status != "workflow running" || !strings.Contains(m.transcript.String(), "demo-flow") {
			t.Fatalf("input=%q status=%q command=%v transcript=%q", input, m.status, command != nil, m.transcript.String())
		}
		updated, followup := m.Update(command())
		m = updated.(*model)
		if followup != nil || m.status != "workflow started" && m.status != "workflow completed" || !strings.Contains(m.transcript.String(), "started in the background") {
			t.Fatalf("input=%q status=%q followup=%v transcript=%q", input, m.status, followup != nil, m.transcript.String())
		}
		wantTUIWorkflowRun(t, registry, "demo-flow", "completed")
	}
}

func wantTUIWorkflowRun(t *testing.T, registry *tools.Registry, name, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, run := range registry.WorkflowRuns() {
			if run.Name == name && run.Status == status {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("workflow %q status %q not found: %+v", name, status, registry.WorkflowRuns())
}

func TestWorkflowSlashCommandsDoNotShadowBuiltinsOrSkills(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model", "deploy", "triage-flakes"} {
		script := "let meta = #{\n  name: \"" + name + "\",\n  description: \"Run " + name + "\",\n};\n"
		if err := os.WriteFile(filepath.Join(workflowDir, name+".rhai"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skillDir := filepath.Join(root, ".grok", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy skill\nuser-invocable: true\n---\nRun deploy.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	m := &model{workspace: root, runner: &agent.Runner{Tools: registry, Skills: catalog}}
	commands := m.namedWorkflowCommands()
	if _, ok := commands["triage-flakes"]; !ok || commands["model"].Name != "" || commands["deploy"].Name != "" {
		t.Fatalf("commands=%#v", commands)
	}
	m.setInput("/tri")
	suggestions := m.slashSuggestions()
	if len(suggestions) == 0 || suggestions[0].insert != "/triage-flakes " || !strings.HasPrefix(suggestions[0].description, "Workflow:") {
		t.Fatalf("suggestions=%#v", suggestions)
	}
}

func TestWorkflowRunListAndStop(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".grok", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "let meta = #{\n  name: \"blocking-flow\",\n  description: \"Wait until stopped\",\n};\n"
	if err := os.WriteFile(filepath.Join(dir, "blocking-flow.rhai"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	t.Setenv("GORK_WORKFLOW_RUNNER", buildTUIWorkflowRunner(t))
	if _, err := registry.Execute(context.Background(), "workflow", []byte(`{"name":"blocking-flow"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var runID string
	for time.Now().Before(deadline) && runID == "" {
		for _, run := range registry.WorkflowRuns() {
			if run.Name == "blocking-flow" && run.Status == "running" {
				runID = run.ID
			}
		}
		time.Sleep(time.Millisecond)
	}
	m := &model{runner: &agent.Runner{Tools: registry}}
	if runID == "" || !strings.Contains(m.listWorkflowRuns(), runID) {
		t.Fatalf("runID=%q list=%q", runID, m.listWorkflowRuns())
	}
	if message := m.handleWorkflowSlash([]string{runID, "stop"}); !strings.Contains(message, "stopping") {
		t.Fatalf("stop=%q", message)
	}
	wantTUIWorkflowRun(t, registry, "blocking-flow", "cancelled")
}

func TestWorkflowRunEventReportsCompletion(t *testing.T) {
	m := &model{}
	updated, command := m.update(workflowRunEvent{run: workflow.RunSnapshot{Name: "review", Status: "completed", Result: "all checks passed"}})
	m = updated.(*model)
	if command == nil || m.status != "workflow completed" || !strings.Contains(m.transcript.String(), "all checks passed") {
		t.Fatalf("command=%v status=%q transcript=%q", command != nil, m.status, m.transcript.String())
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
	"time"
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
	} else if strings.Contains(script, "name: \"blocking-flow\"") {
		for { time.Sleep(time.Hour) }
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
