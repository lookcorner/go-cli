package acp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
