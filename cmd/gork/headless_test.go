package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
)

func TestLoadHeadlessPromptSupportsTextAndACPJSONFiles(t *testing.T) {
	root := t.TempDir()
	textPath := filepath.Join(root, "prompt.txt")
	jsonPath := filepath.Join(root, "prompt.json")
	if err := os.WriteFile(textPath, []byte("  inspect this  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, []byte(`{"type":"acp","content":[
		{"type":"text","text":"inspect image"},
		{"type":"image","mimeType":"image/png","data":"cG5n"},
		{"type":"resource_link","name":"README","uri":"file:///README.md"},
		{"type":"resource","resource":{"uri":"file:///notes.txt","mimeType":"text/plain","text":"notes"}}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	textPrompt, ok, err := loadHeadlessPrompt(options{promptFile: textPath, promptFileSet: true})
	if err != nil || !ok || textPrompt.text != "inspect this" || len(textPrompt.parts) != 0 {
		t.Fatalf("text prompt=%#v ok=%v err=%v", textPrompt, ok, err)
	}
	jsonPrompt, ok, err := loadHeadlessPrompt(options{promptFile: jsonPath, promptFileSet: true})
	if err != nil || !ok || !strings.Contains(jsonPrompt.text, "Embedded resource file:///notes.txt") || len(jsonPrompt.parts) != 4 {
		t.Fatalf("json prompt=%#v ok=%v err=%v", jsonPrompt, ok, err)
	}
	if jsonPrompt.parts[0] != (api.ContentPart{Type: "input_text", Text: "inspect image"}) ||
		jsonPrompt.parts[1] != (api.ContentPart{Type: "input_image", ImageURL: "data:image/png;base64,cG5n"}) {
		t.Fatalf("parts=%#v", jsonPrompt.parts)
	}
	if jsonPrompt.parts[2].Text != "Referenced resource README: file:///README.md" ||
		jsonPrompt.parts[3].Text != "Embedded resource file:///notes.txt (text/plain):\nnotes" {
		t.Fatalf("resource parts=%#v", jsonPrompt.parts)
	}
}

func TestLoadHeadlessPromptRejectsInvalidInputs(t *testing.T) {
	tests := []options{
		{singleSet: true},
		{promptJSON: `[]`, promptJSONSet: true},
		{promptJSON: `{"type":"other","content":[]}`, promptJSONSet: true},
		{promptJSON: `[{"type":"audio"}]`, promptJSONSet: true},
		{promptJSON: `[{"type":"resource","resource":{"uri":"file:///binary","blob":"AA=="}}]`, promptJSONSet: true},
	}
	for _, opts := range tests {
		if _, _, err := loadHeadlessPrompt(opts); err == nil {
			t.Fatalf("options=%#v unexpectedly passed", opts)
		}
	}
}

func TestParseJSONSchemaRequiresObject(t *testing.T) {
	for _, value := range []string{`[]`, `"text"`, `null`, `{broken`} {
		if _, err := parseJSONSchema(value); err == nil {
			t.Fatalf("schema %q unexpectedly passed", value)
		}
	}
	schema, err := parseJSONSchema(`{"type":"object"}`)
	if err != nil || schema["type"] != "object" {
		t.Fatalf("schema=%#v err=%v", schema, err)
	}
}

func TestHeadlessEmitterMachineReadableError(t *testing.T) {
	var output bytes.Buffer
	emitter := &headlessEmitter{format: headlessOutputJSON, output: &output}
	emitter.emitError(errors.New("request failed"))
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "error" || payload["message"] != "request failed" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestHeadlessEmitterJSONFormats(t *testing.T) {
	result := agent.Result{
		ResponseID: "response-1", Text: `{"name":"gork"}`,
		Usage: &api.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}
	var jsonOutput bytes.Buffer
	jsonEmitter := &headlessEmitter{format: headlessOutputJSON, output: &jsonOutput, sessionID: "session-1"}
	if err := jsonEmitter.add(result); err != nil {
		t.Fatal(err)
	}
	if err := jsonEmitter.finish(true); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output %q: %v", jsonOutput.String(), err)
	}
	structured := payload["structuredOutput"].(map[string]any)
	if payload["text"] != result.Text || payload["stopReason"] != "EndTurn" ||
		payload["sessionId"] != "session-1" || payload["requestId"] != "response-1" ||
		structured["name"] != "gork" {
		t.Fatalf("payload=%#v", payload)
	}
	usage := payload["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) ||
		usage["total_tokens"] != float64(5) || payload["num_turns"] != float64(1) {
		t.Fatalf("usage=%#v payload=%#v", usage, payload)
	}

	var streaming bytes.Buffer
	streamEmitter := &headlessEmitter{format: headlessOutputStreamingJSON, output: &streaming, sessionID: "session-1"}
	writer := streamEmitter.textWriter()
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := streamEmitter.add(result); err != nil {
		t.Fatal(err)
	}
	if err := streamEmitter.finish(false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(streaming.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"type":"text"`) || !strings.Contains(lines[1], `"type":"end"`) {
		t.Fatalf("streaming output=%q", streaming.String())
	}
}
