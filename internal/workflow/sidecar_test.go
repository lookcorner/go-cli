package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type stubSpawner struct {
	last AgentOpts
	res  AgentResult
	err  error
}

func (s *stubSpawner) SpawnAgent(_ context.Context, opts AgentOpts) (AgentResult, error) {
	s.last = opts
	if s.err != nil {
		return AgentResult{}, s.err
	}
	if s.res.AgentID == "" {
		return AgentResult{
			AgentID: "agent-1", Success: true,
			Output: json.RawMessage(`{"ok":true}`), DurationMS: 3,
		}, nil
	}
	return s.res, nil
}

func TestHostSpawnAgentAndScratch(t *testing.T) {
	spawner := &stubSpawner{}
	host := &Host{Spawner: spawner, Scratch: t.TempDir(), agentBudget: 4}
	reply := host.HandleRequest(context.Background(), HostRequest{
		Type: MsgHostReq, ID: "1", Kind: "reserve_agent_calls", Count: 1,
	})
	if !reply.OK {
		t.Fatalf("reserve: %+v", reply.Error)
	}
	reply = host.HandleRequest(context.Background(), HostRequest{
		Type: MsgHostReq, ID: "2", Kind: "spawn_agent",
		Opts: &AgentOpts{Prompt: "do work", Label: "lab", CapabilityMode: "read-only", AgentType: "general-purpose"},
	})
	if !reply.OK {
		t.Fatalf("spawn: %+v", reply.Error)
	}
	if spawner.last.Prompt != "do work" || spawner.last.CapabilityMode != "read-only" {
		t.Fatalf("spawn opts: %+v", spawner.last)
	}
	reply = host.HandleRequest(context.Background(), HostRequest{
		Type: MsgHostReq, ID: "3", Kind: "write_scratch_file", Name: "note.txt", Content: "hello",
	})
	if !reply.OK {
		t.Fatalf("write: %+v", reply.Error)
	}
	reply = host.HandleRequest(context.Background(), HostRequest{
		Type: MsgHostReq, ID: "4", Kind: "read_scratch_file", Name: "note.txt",
	})
	if !reply.OK || string(reply.Result) != `"hello"` {
		t.Fatalf("read: ok=%v result=%s err=%+v", reply.OK, reply.Result, reply.Error)
	}
}

func TestRunWithSidecarFakeRunner(t *testing.T) {
	runner := buildFakeRunner(t)
	spawner := &stubSpawner{}
	var phases []string
	host := &Host{
		Spawner: spawner,
		Callbacks: HostCallbacks{
			OnPhase: func(title string, _ bool) { phases = append(phases, title) },
		},
	}
	outcome, err := RunWithSidecar(context.Background(), runner, "let meta = #{ name: \"x\", description: \"d\" };", json.RawMessage(`{"q":"1"}`), host)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != "completed" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if spawner.last.Prompt == "" {
		t.Fatal("expected spawn_agent prompt")
	}
	if len(phases) != 1 || phases[0] != "Demo" {
		t.Fatalf("phases=%v", phases)
	}
	text, err := FormatOutcome(outcome)
	if err != nil || !strings.Contains(text, "workflow completed") {
		t.Fatalf("format=%q err=%v", text, err)
	}
}

func TestRunWithSidecarPreservesTypedArgs(t *testing.T) {
	runner := buildFakeRunner(t)
	outcome, err := RunWithSidecar(
		context.Background(), runner,
		"let meta = #{ name: \"x\", description: \"d\" };",
		json.RawMessage(`{"breadth":3,"strict":true}`),
		&Host{Spawner: &stubSpawner{}},
	)
	if err != nil || outcome.Outcome != "completed" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestRunWithSidecarPreservesNullArgs(t *testing.T) {
	runner := buildFakeRunner(t)
	outcome, err := RunWithSidecar(
		context.Background(), runner,
		"let meta = #{ name: \"no-args\", description: \"d\" };",
		nil,
		&Host{Spawner: &stubSpawner{}},
	)
	if err != nil || outcome.Outcome != "completed" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestRunWithSidecarRejectsInvalidArgsBeforeStart(t *testing.T) {
	_, err := RunWithSidecar(
		context.Background(), "unused-runner",
		"let meta = #{ name: \"x\", description: \"d\" };",
		json.RawMessage(`{"broken"`),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err=%v", err)
	}
}

func TestExecuteRequiresRunner(t *testing.T) {
	t.Setenv(envWorkflowRunner, "")
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = exec.LookPath })
	_, err := Execute(context.Background(), Resolved{
		Name: "x", Source: "inline", Script: `let meta = #{
  name: "x",
  description: "demo",
};
`,
	}, nil, &Host{Spawner: &stubSpawner{}})
	if err == nil || !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestExecuteEmbeddedDeepResearch(t *testing.T) {
	runner := buildFakeRunner(t)
	t.Setenv(envWorkflowRunner, runner)
	resolved, err := ResolveByName(t.TempDir(), "deep-research")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), resolved, json.RawMessage(`{"query":"verify this"}`), &Host{Spawner: &stubSpawner{}})
	if err != nil || !strings.Contains(result, "workflow completed") {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestResolveRunnerBinaryEnv(t *testing.T) {
	runner := buildFakeRunner(t)
	t.Setenv(envWorkflowRunner, runner)
	got, err := ResolveRunnerBinary()
	if err != nil || got != runner {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func buildFakeRunner(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "fake_runner.go")
	bin := filepath.Join(dir, "fake_runner")
	code := `package main
import (
  "bufio"
  "encoding/json"
  "os"
	"strings"
)
func main() {
  in := bufio.NewScanner(os.Stdin)
  if !in.Scan() { os.Exit(2) }
  var start map[string]any
  if json.Unmarshal(in.Bytes(), &start) != nil || start["type"] != "start" { os.Exit(3) }
	if strings.Contains(start["script"].(string), "name: \"no-args\"") {
		if start["args"] != nil { os.Exit(7) }
	} else {
		args, ok := start["args"].(map[string]any)
		if !ok { os.Exit(7) }
		if args["breadth"] != nil && (args["breadth"] != float64(3) || args["strict"] != true) { os.Exit(7) }
	}
  enc := json.NewEncoder(os.Stdout)
  _ = enc.Encode(map[string]any{"type":"host_notify","kind":"phase","title":"Demo"})
  _ = enc.Encode(map[string]any{
    "type":"host_request","id":"1","kind":"reserve_agent_calls","count":1,
  })
  if !in.Scan() { os.Exit(4) }
  _ = enc.Encode(map[string]any{
    "type":"host_request","id":"2","kind":"spawn_agent",
    "opts": map[string]any{"prompt":"from-fake-runner","capability_mode":"read-only","agent_type":"general-purpose"},
  })
  if !in.Scan() { os.Exit(5) }
  var reply map[string]any
  _ = json.Unmarshal(in.Bytes(), &reply)
  if reply["ok"] != true { os.Exit(6) }
  _ = enc.Encode(map[string]any{
    "type":"outcome","outcome":"completed","result":map[string]any{"ok":true},
  })
}
`
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake runner: %v\n%s", err, out)
	}
	return bin
}
