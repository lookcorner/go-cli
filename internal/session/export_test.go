package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportMarkdownIncludesConversationAndCompactToolSummaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.jsonl")
	arguments, _ := json.Marshal(map[string]any{"command": "go test ./..."})
	editArguments, _ := json.Marshal(map[string]any{"file_path": "main.go"})
	content := "" +
		`{"kind":"user_prompt","data":{"text":"run tests"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"Starting.","tool_call_count":1}}` + "\n" +
		`{"kind":"tool_call","data":{"call_id":"call-1","name":"shell","arguments":` + string(arguments) + `}}` + "\n" +
		`{"kind":"tool_result","data":{"call_id":"call-1","name":"shell","output":"ok"}}` + "\n" +
		`{"kind":"tool_call","data":{"call_id":"call-2","name":"hashline_edit","arguments":` + string(editArguments) + `}}` + "\n" +
		`{"kind":"tool_result","data":{"call_id":"call-2","name":"hashline_edit","output":"updated"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r2","text":"Done.","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := ExportMarkdown(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"## User\n\nrun tests",
		"## Assistant\n\nStarting.",
		"## Tools\n\n- Execute: go test ./...",
		"- Edit: main.go",
		"## Assistant\n\nDone.",
	} {
		if !strings.Contains(exported, expected) {
			t.Fatalf("export missing %q:\n%s", expected, exported)
		}
	}
	if strings.Contains(exported, `"call_id"`) || strings.Contains(exported, "output") {
		t.Fatalf("export leaked tool protocol details:\n%s", exported)
	}
}

func TestExportMarkdownSkipsSyntheticPromptsAndFormatsImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"hidden","synthetic":true}}` + "\n" +
		`{"kind":"user_prompt","data":{"text":"inspect","content":[{"type":"text","text":"inspect"},{"type":"image","uri":"asset.png"},{"type":"image","uri":"https://example.com/image.png"}]}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"done","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := ExportMarkdown(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(exported, "hidden") || !strings.Contains(exported, "inspect\n[Image]\n[Image: https://example.com/image.png]") {
		t.Fatalf("unexpected image export:\n%s", exported)
	}
}

func TestExportMarkdownRequiresCompletedConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"user_prompt","data":{"text":"pending"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportMarkdown(path); err == nil {
		t.Fatal("incomplete session was exported")
	}
}
