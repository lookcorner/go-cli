package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
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

func TestNormalizeOptionalValueArgs(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"--resume"}, []string{"--resume="}},
		{[]string{"-r", "--config", "config.toml"}, []string{"-r=", "--config", "config.toml"}},
		{[]string{"--resume", "--", "-prompt"}, []string{"--resume=", "--", "-prompt"}},
		{[]string{"--resume", "session-id"}, []string{"--resume", "session-id"}},
		{[]string{"--resume=session-id"}, []string{"--resume=session-id"}},
		{[]string{"--worktree"}, []string{"--worktree="}},
		{[]string{"-w", "--resume", "parent"}, []string{"-w=", "--resume", "parent"}},
		{[]string{"--worktree", "feature"}, []string{"--worktree", "feature"}},
	}
	for _, test := range tests {
		got := normalizeOptionalValueArgs(test.args)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("args=%v got=%v want=%v", test.args, got, test.want)
		}
	}
}

func TestParseRunOptionsSupportsSessionStartupAliases(t *testing.T) {
	tests := []struct {
		args          []string
		resume        string
		resumeSet     bool
		continueLast  bool
		sessionID     string
		fork          bool
		worktreeSet   bool
		worktree      string
		worktreeRef   string
		noPlan        bool
		noSubagents   bool
		noAskUser     bool
		disableWeb    bool
		positionalLen int
	}{
		{args: []string{"--resume"}, resumeSet: true},
		{args: []string{"-r", "parent"}, resume: "parent", resumeSet: true},
		{args: []string{"--load", "parent"}, resume: "parent", resumeSet: true},
		{args: []string{"-c"}, continueLast: true},
		{args: []string{"-s", "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40"}, sessionID: "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40"},
		{args: []string{"--resume", "--", "-prompt"}, resumeSet: true, positionalLen: 1},
		{args: []string{"--resume", "parent", "--fork-session"}, resume: "parent", resumeSet: true, fork: true},
		{args: []string{"-w", "--ref", "main"}, worktreeSet: true, worktreeRef: "main"},
		{args: []string{"--worktree", "feature", "--worktree-ref", "HEAD"}, worktreeSet: true, worktree: "feature", worktreeRef: "HEAD"},
		{args: []string{"--no-plan", "--no-subagents", "--no-ask-user", "--disable-web-search"}, noPlan: true, noSubagents: true, noAskUser: true, disableWeb: true},
	}
	for _, test := range tests {
		opts, flags, err := parseRunOptions(test.args, io.Discard)
		if err != nil {
			t.Fatalf("args=%v err=%v", test.args, err)
		}
		if opts.resume != test.resume || opts.resumeSet != test.resumeSet ||
			opts.continueLast != test.continueLast || opts.sessionID != test.sessionID ||
			opts.forkSession != test.fork || opts.worktreeSet != test.worktreeSet ||
			opts.worktree != test.worktree || opts.worktreeRef != test.worktreeRef ||
			opts.noPlan != test.noPlan || opts.noSubagents != test.noSubagents ||
			opts.noAskUser != test.noAskUser || opts.disableWebSearch != test.disableWeb ||
			len(flags.Args()) != test.positionalLen {
			t.Fatalf("args=%v opts=%#v positional=%v", test.args, opts, flags.Args())
		}
	}
}

func TestResolveSessionStartup(t *testing.T) {
	dir, firstRoot, secondRoot := t.TempDir(), t.TempDir(), t.TempDir()
	writeSession := func(id, cwd string, title ...string) {
		logger, err := session.NewLoggerWithID(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "test"}); err != nil {
			t.Fatal(err)
		}
		if len(title) > 0 {
			if err := logger.Append("session_title", map[string]any{"title": title[0]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeSession("first", firstRoot)
	writeSession("titled", firstRoot, "Fix Login Bug")
	time.Sleep(time.Millisecond)
	writeSession("second", firstRoot)
	writeSession("other", secondRoot)

	opts := options{sessionDir: dir, continueLast: true}
	startup, err := resolveSessionStartup(opts, firstRoot)
	if err != nil || filepath.Base(startup.resumePath) != "second.jsonl" {
		t.Fatalf("continue startup=%#v err=%v", startup, err)
	}
	startup, err = resolveSessionStartup(options{sessionDir: dir, resume: "first", resumeSet: true}, firstRoot)
	if err != nil || filepath.Base(startup.resumePath) != "first.jsonl" {
		t.Fatalf("ID startup=%#v err=%v", startup, err)
	}
	startup, err = resolveSessionStartup(options{sessionDir: dir, resume: " fix login bug ", resumeSet: true}, firstRoot)
	if err != nil || filepath.Base(startup.resumePath) != "titled.jsonl" {
		t.Fatalf("title startup=%#v err=%v", startup, err)
	}
	writeSession("Fix-Login-Bug", firstRoot)
	startup, err = resolveSessionStartup(options{sessionDir: dir, resume: "Fix-Login-Bug", resumeSet: true}, firstRoot)
	if err != nil || filepath.Base(startup.resumePath) != "Fix-Login-Bug.jsonl" {
		t.Fatalf("ID precedence startup=%#v err=%v", startup, err)
	}
	uuidTitle := "12345678-1234-1234-1234-123456789abc"
	writeSession("uuid-title", firstRoot, uuidTitle)
	startup, err = resolveSessionStartup(options{sessionDir: dir, resume: uuidTitle, resumeSet: true}, firstRoot)
	if err != nil || filepath.Base(startup.resumePath) != uuidTitle+".jsonl" {
		t.Fatalf("UUID precedence startup=%#v err=%v", startup, err)
	}
	id := "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40"
	startup, err = resolveSessionStartup(options{
		sessionDir: dir, resume: "first", resumeSet: true, forkSession: true, sessionID: id,
	}, firstRoot)
	if err != nil || !startup.fork || startup.newID != id || filepath.Base(startup.resumePath) != "first.jsonl" {
		t.Fatalf("fork startup=%#v err=%v", startup, err)
	}
}

func TestResolveSessionStartupRejectsInvalidCombinations(t *testing.T) {
	root := t.TempDir()
	tests := []options{
		{continueLast: true, resumeSet: true, resume: "parent"},
		{sessionID: "not-a-uuid"},
		{sessionID: "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40", resumeSet: true, resume: "parent"},
		{forkSession: true},
		{forkSession: true, resumeSet: true, resume: "parent", worktreeSet: true},
		{worktreeRef: "main"},
	}
	for _, opts := range tests {
		if _, err := resolveSessionStartup(opts, root); err == nil {
			t.Fatalf("options=%#v unexpectedly passed", opts)
		}
	}
	if _, err := resolveSessionStartup(options{sessionDir: t.TempDir(), continueLast: true}, root); err == nil {
		t.Fatal("continue without a workspace session passed")
	}
}

func TestNewSessionUUID(t *testing.T) {
	first, err := newSessionUUID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSessionUUID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !sessionUUIDPattern.MatchString(first) || first[14] != '4' ||
		!strings.ContainsRune("89ab", rune(strings.ToLower(first)[19])) {
		t.Fatalf("UUIDs first=%q second=%q", first, second)
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
	cost := int64(1_234_500_000)
	result := agent.Result{
		ResponseID: "response-1", Text: `{"name":"gork"}`, Model: "grok-4.5",
		Usage: &api.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5, CostUSDTicks: &cost},
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
	modelUsage := payload["modelUsage"].(map[string]any)["grok-4.5"].(map[string]any)
	if payload["total_cost_usd"] != 0.12345 || payload["total_cost_usd_ticks"] != float64(cost) || modelUsage["costUSD"] != 0.12345 || modelUsage["modelCalls"] != float64(1) {
		t.Fatalf("cost payload=%#v", payload)
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

func TestHeadlessEmitterAggregatesEveryModelCallAndHidesPartialCost(t *testing.T) {
	cost := int64(400)
	result := agent.Result{UsageHistory: []agent.ModelUsage{
		{Model: "grok", Usage: api.Usage{InputTokens: 10, CachedReadTokens: 4, OutputTokens: 2, TotalTokens: 12, CostUSDTicks: &cost}},
		{Model: "fast", Usage: api.Usage{InputTokens: 20, CachedReadTokens: 5, OutputTokens: 3, TotalTokens: 23}},
	}}
	var output bytes.Buffer
	emitter := &headlessEmitter{format: headlessOutputJSON, output: &output}
	if err := emitter.add(result); err != nil {
		t.Fatal(err)
	}
	if err := emitter.finish(false); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	usage := payload["usage"].(map[string]any)
	model := payload["modelUsage"].(map[string]any)["grok"].(map[string]any)
	fast := payload["modelUsage"].(map[string]any)["fast"].(map[string]any)
	if payload["num_turns"] != float64(2) || usage["input_tokens"] != float64(21) || usage["cache_read_input_tokens"] != float64(9) || usage["output_tokens"] != float64(5) || model["modelCalls"] != float64(1) || fast["modelCalls"] != float64(1) {
		t.Fatalf("payload=%#v", payload)
	}
	if payload["cost_is_partial"] != true || payload["total_cost_usd"] != nil || payload["total_cost_usd_ticks"] != nil || model["costUSD"] != nil || fast["costUSD"] != nil {
		t.Fatalf("partial cost leaked: %#v", payload)
	}
}

func TestHeadlessEmitterDoesNotCallUnknownCostPartial(t *testing.T) {
	result := agent.Result{Model: "grok", Usage: &api.Usage{InputTokens: 3, OutputTokens: 2}}
	var output bytes.Buffer
	emitter := &headlessEmitter{format: headlessOutputJSON, output: &output}
	if err := emitter.add(result); err != nil {
		t.Fatal(err)
	}
	if err := emitter.finish(false); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["cost_is_partial"] != nil || payload["total_cost_usd"] != nil || payload["total_cost_usd_ticks"] != nil {
		t.Fatalf("unknown cost was presented as known or partial: %#v", payload)
	}
}
