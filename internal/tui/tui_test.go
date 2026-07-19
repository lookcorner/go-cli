package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type scheduledTUIStreamer struct {
	request api.ResponseRequest
}

func (s *scheduledTUIStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{ResponseID: "scheduled-response", Text: "checked"}, nil
}

func TestBridgeApproval(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	result := make(chan error, 1)
	go func() { result <- bridge.Approve(context.Background(), "shell", "go test ./...") }()
	var request approvalEvent
	select {
	case message := <-bridge.events:
		var ok bool
		request, ok = message.(approvalEvent)
		if !ok {
			t.Fatalf("unexpected event: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("approval request did not arrive")
	}
	request.reply <- true
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not complete")
	}
}

func TestModelInputAndView(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge,
		workspace: "/workspace", modelName: "test-model", width: 60, height: 16, status: "ready",
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: '你', Text: "你"}))
	m = updated.(*model)
	if string(m.input) != "你" {
		t.Fatalf("unexpected input: %q", m.input)
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || len(m.input) != 0 {
		t.Fatalf("submit did not start turn: running=%v input=%q command=%v", m.running, m.input, command)
	}
	view := m.View()
	if !view.AltScreen || !strings.Contains(view.Content, "Gork Go") || !strings.Contains(view.Content, "你") {
		t.Fatalf("unexpected view: %#v", view)
	}
}

func TestSliceFromBottom(t *testing.T) {
	lines := []string{"1", "2", "3", "4", "5"}
	if got := strings.Join(sliceFromBottom(lines, 2, 0), ","); got != "4,5" {
		t.Fatalf("unexpected bottom slice: %s", got)
	}
	if got := strings.Join(sliceFromBottom(lines, 2, 2), ","); got != "2,3" {
		t.Fatalf("unexpected scrolled slice: %s", got)
	}
}

func TestCompactCommandDoesNotEnterTranscript(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, previousID: "response-1", status: "ready"}
	m.input = []rune("/compact")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.transcript.Len() != 0 || m.status != "compacting context" {
		t.Fatalf("compact command entered normal turn: running=%v status=%q transcript=%q", m.running, m.status, m.transcript.String())
	}
}

func TestScheduledEventWaitsForTurnAndContinuesResponseChain(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &scheduledTUIStreamer{}
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test"}, bridge: bridge,
		previousID: "parent-response", running: true, status: "thinking",
	}
	event := tools.ScheduledTaskFired{TaskID: "loop-1", Prompt: "check deployment"}
	updated, _ := m.Update(scheduledFiredEvent{event: event})
	m = updated.(*model)
	if len(m.scheduled) != 1 || m.activeTask != "" {
		t.Fatalf("scheduled=%#v active=%q", m.scheduled, m.activeTask)
	}
	updated, command := m.Update(turnDoneEvent{result: agent.Result{ResponseID: "user-response"}})
	m = updated.(*model)
	if command == nil || !m.running || m.activeTask != "loop-1" || m.previousID != "user-response" {
		t.Fatalf("running=%v active=%q previous=%q command=%v", m.running, m.activeTask, m.previousID, command)
	}
	updated, _ = m.Update(scheduledFiredEvent{event: event})
	m = updated.(*model)
	if len(m.scheduled) != 0 {
		t.Fatalf("active scheduled task was duplicated: %#v", m.scheduled)
	}
	message := command()
	done, ok := message.(turnDoneEvent)
	if !ok || done.err != nil || done.result.ResponseID != "scheduled-response" {
		t.Fatalf("turn result=%#v", message)
	}
	input, _ := json.Marshal(streamer.request.Input)
	if streamer.request.PreviousResponseID != "user-response" || !strings.Contains(string(input), "check deployment") {
		t.Fatalf("request=%#v input=%s", streamer.request, input)
	}
}

func TestRenderMarkdownStylesAndWrapsVisibleText(t *testing.T) {
	lines := renderMarkdown("# Heading\n\n- **bold** and `code`\n> quoted\n[docs](https://example.com)\n```go\n你好abc\n```", 6)
	rendered := strings.Join(lines, "\n")
	for _, expected := range []string{ansiBold, ansiCyan, ansiYellow, ansiUnderline, "• ", "bold", "│ ", "docs", "你好"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered markdown missing %q:\n%s", expected, rendered)
		}
	}
	plain := stripMarkdownANSI(rendered)
	flat := strings.ReplaceAll(plain, "\n", "")
	for _, expected := range []string{"Heading", "code", "https://example.com", "你好abc"} {
		if !strings.Contains(flat, expected) {
			t.Fatalf("rendered markdown lost %q: %q", expected, plain)
		}
	}
	for _, line := range lines {
		if markdownVisibleWidth(line) > 6 {
			t.Fatalf("line exceeded visible width: %q", line)
		}
	}
}

func TestRenderMarkdownKeepsIncompleteStreamingMarkers(t *testing.T) {
	rendered := strings.Join(renderMarkdown("partial **bold", 80), "\n")
	if !strings.Contains(rendered, "partial **bold") {
		t.Fatalf("incomplete streaming markdown was lost: %q", rendered)
	}
}

func TestMarkdownVisibleWidthHandlesEmojiAndCombiningMarks(t *testing.T) {
	if width := markdownVisibleWidth("A🙂e\u0301"); width != 4 {
		t.Fatalf("visible width=%d want=4", width)
	}
}

func TestRenderMarkdownMakesProgressBelowWideRuneWidth(t *testing.T) {
	lines := renderMarkdown("你", 1)
	if len(lines) != 1 || !strings.Contains(lines[0], "你") {
		t.Fatalf("wide rune was not rendered: %#v", lines)
	}
}

func markdownVisibleWidth(value string) int {
	plain := stripMarkdownANSI(value)
	width := 0
	for _, r := range plain {
		width += runeWidth(r)
	}
	return width
}

func stripMarkdownANSI(value string) string {
	plain := value
	for _, sequence := range []string{ansiReset, ansiBold, ansiDim, ansiItalic, ansiUnderline, ansiCyan, ansiYellow} {
		plain = strings.ReplaceAll(plain, sequence, "")
	}
	return plain
}
