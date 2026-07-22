package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/lsp"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type codeNavFixture struct {
	servers int
	root    string
}

func (f *codeNavFixture) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: "code-nav-fixture"}
}
func (f *codeNavFixture) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
func (f *codeNavFixture) CodeNavigationServers() int { return f.servers }
func (f *codeNavFixture) CodeLocations(_ context.Context, operation, _ string, _, _ int) ([]lsp.Location, error) {
	line := 4
	if operation == "references" {
		line = 8
	}
	return []lsp.Location{{Path: filepath.Join(f.root, "main.go"), Range: lsp.Range{
		Start: lsp.Position{Line: line, Character: 2}, End: lsp.Position{Line: line, Character: 6},
	}}}, nil
}
func (f *codeNavFixture) CodeSymbols(context.Context, string) ([]lsp.Symbol, error) {
	return []lsp.Symbol{{Name: "Target", Location: lsp.Location{Path: filepath.Join(f.root, "main.go"), Range: lsp.Range{
		Start: lsp.Position{Line: 4, Character: 2}, End: lsp.Position{Line: 4, Character: 8},
	}}}}, nil
}

func TestCodeNavigationExtensionWireContract(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.Register(&codeNavFixture{servers: 1, root: root}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	current := &session{id: "nav-session", cwd: root, runner: &agent.Runner{Tools: registry}}
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}

	requests := []message{
		{ID: json.RawMessage("1"), Method: "x.ai/code/status", Params: json.RawMessage(`{"sessionId":"nav-session"}`)},
		{ID: json.RawMessage("2"), Method: "x.ai/code/goto-definition", Params: json.RawMessage(`{"sessionId":"nav-session","path":"main.go","row":1,"column":1}`)},
		{ID: json.RawMessage("3"), Method: "x.ai/code/find-definitions", Params: json.RawMessage(`{"sessionId":"nav-session","symbol":"Target","contextPath":"main.go"}`)},
		{ID: json.RawMessage("4"), Method: "x.ai/code/find-references", Params: json.RawMessage(`{"sessionId":"nav-session","symbol":"Target"}`)},
	}
	for _, request := range requests {
		server.handleCodeNavigation(context.Background(), request)
	}
	messages := decodeACPOutput(t, output.Bytes())
	status := messages[0]["result"].(map[string]any)["result"].(map[string]any)
	if status["indexed"] != true || status["eligible"] != true || status["reason"] != "active" {
		t.Fatalf("unexpected status: %#v", status)
	}
	definition := messages[1]["result"].(map[string]any)["result"].(map[string]any)["locations"].([]any)[0].(map[string]any)
	if definition["path"] != filepath.Join(root, "main.go") || definition["line"] != float64(5) || definition["column"] != float64(3) {
		t.Fatalf("unexpected definition: %#v", definition)
	}
	findDefinition := messages[2]["result"].(map[string]any)["result"].(map[string]any)
	if findDefinition["symbol"] != "Target" || findDefinition["locations"].([]any)[0].(map[string]any)["matchedSymbol"] != "Target" {
		t.Fatalf("unexpected find definition: %#v", findDefinition)
	}
	findReference := messages[3]["result"].(map[string]any)["result"].(map[string]any)["locations"].([]any)[0].(map[string]any)
	if findReference["line"] != float64(9) || findReference["endColumn"] != float64(7) {
		t.Fatalf("unexpected find reference: %#v", findReference)
	}
}

func TestCodeNavigationRequiresSessionAndReportsUnavailable(t *testing.T) {
	root := t.TempDir()
	ws, _ := workspace.Open(root)
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{
		"without-lsp": {id: "without-lsp", cwd: root, runner: &agent.Runner{Tools: registry}},
	}}
	server.handleCodeNavigation(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/code/status", Params: json.RawMessage(`{"sessionId":"missing"}`)})
	server.handleCodeNavigation(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/code/status", Params: json.RawMessage(`{"sessionId":"without-lsp"}`)})
	messages := decodeACPOutput(t, output.Bytes())
	if messages[0]["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("unexpected missing-session response: %#v", messages[0])
	}
	status := messages[1]["result"].(map[string]any)["result"].(map[string]any)
	if status["indexed"] != false || status["eligible"] != false || status["reason"] != "disabledByConfig" {
		t.Fatalf("unexpected unavailable status: %#v", status)
	}
}
