package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type scheduledTUIStreamer struct {
	request api.ResponseRequest
}

type rememberTUIStreamer struct{}

func (rememberTUIStreamer) StreamResponse(_ context.Context, _ api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	return api.StreamResult{Text: "## Deployment\n\n- Run enhanced checks."}, nil
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

func TestBridgeQuestionSelectionAndPlanClarification(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, width: 70, height: 18, running: true}
	request := tools.UserQuestionRequest{ToolCallID: "ask-1", Mode: "plan", Questions: []tools.UserQuestion{
		{Question: "Which database?", Options: []tools.UserQuestionOption{{Label: "SQLite", Preview: "schema"}}},
		{Question: "Which region?", Options: []tools.UserQuestionOption{{Label: "Local"}}},
	}}
	result := make(chan tools.UserQuestionResponse, 1)
	go func() {
		response, _ := bridge.AskUserQuestion(context.Background(), request)
		result <- response
	}()
	var event questionEvent
	select {
	case message := <-bridge.events:
		event = message.(questionEvent)
	case <-time.After(time.Second):
		t.Fatal("question event did not arrive")
	}
	updated, _ := m.Update(event)
	m = updated.(*model)
	if !strings.Contains(m.View().Content, "Which database?") || !strings.Contains(m.View().Content, "SQLite") {
		t.Fatalf("question view=%q", m.View().Content)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '1', Text: "1"}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.question == nil || m.question.index != 1 || m.status != "question 2/2" {
		t.Fatalf("question=%#v status=%q", m.question, m.status)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m = updated.(*model)
	select {
	case response := <-result:
		if response.Outcome != "chat_about_this" || response.PartialAnswers["Which database?"] != "SQLite" {
			t.Fatalf("response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("question did not complete")
	}
	if m.question != nil || m.status != "thinking" {
		t.Fatalf("question=%#v status=%q", m.question, m.status)
	}
}

func TestBridgeQuestionAccepted(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, width: 60, height: 16, running: true}
	result := make(chan tools.UserQuestionResponse, 1)
	go func() {
		response, _ := bridge.AskUserQuestion(context.Background(), tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
			Question: "Deploy where?", Options: []tools.UserQuestionOption{{Label: "Local"}},
		}}})
		result <- response
	}()
	event := (<-bridge.events).(questionEvent)
	updated, _ := m.Update(event)
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '1', Text: "1"}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	select {
	case response := <-result:
		if response.Outcome != "accepted" || response.Answers["Deploy where?"][0] != "Local" {
			t.Fatalf("response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted question did not complete")
	}
}

func TestBridgePlanModeReviewRequestsChanges(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, width: 72, height: 18, status: "ready"}
	bridge.PlanModeEntered(tools.PlanModeEvent{})
	updated, _ := m.Update(<-bridge.events)
	m = updated.(*model)
	if !m.planMode || !strings.Contains(m.View().Content, " PLAN ") {
		t.Fatalf("plan header=%q", m.View().Content)
	}

	result := make(chan tools.PlanModeDecision, 1)
	go func() {
		decision, _ := bridge.ApprovePlanModeExit(context.Background(), tools.PlanModeEvent{PlanContent: "# Plan\n\n1. Extract core"})
		result <- decision
	}()
	updated, _ = m.Update(<-bridge.events)
	m = updated.(*model)
	view := m.View().Content
	if !strings.Contains(view, "Extract core") || !strings.Contains(view, "Plan review") {
		t.Fatalf("review view=%q", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	m = updated.(*model)
	for _, char := range "split I/O first" {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)}))
		m = updated.(*model)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	select {
	case decision := <-result:
		if decision.Outcome != "cancelled" || decision.Feedback != "split I/O first" {
			t.Fatalf("decision=%#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("plan review did not complete")
	}
	if !m.planMode || m.planReview != nil {
		t.Fatalf("planMode=%v review=%#v", m.planMode, m.planReview)
	}
	bridge.PlanModeExited(tools.PlanModeEvent{})
	updated, _ = m.Update(<-bridge.events)
	m = updated.(*model)
	if m.planMode || strings.Contains(m.View().Content, " PLAN ") {
		t.Fatalf("default header=%q", m.View().Content)
	}
}

func TestPlanReviewTerminalOutcomes(t *testing.T) {
	tests := []struct {
		key     tea.Key
		outcome string
	}{
		{tea.Key{Code: 'y', Text: "y"}, "approved"},
		{tea.Key{Code: 'a', Text: "a"}, "abandoned"},
		{tea.Key{Code: tea.KeyEscape}, "cancelled"},
	}
	for _, test := range tests {
		t.Run(test.outcome, func(t *testing.T) {
			reply := make(chan tools.PlanModeDecision, 1)
			m := &model{planMode: true, planReview: &planReviewState{event: planReviewEvent{reply: reply}}}
			updated, _ := m.Update(tea.KeyPressMsg(test.key))
			m = updated.(*model)
			if decision := <-reply; decision.Outcome != test.outcome || m.planReview != nil {
				t.Fatalf("decision=%#v review=%#v", decision, m.planReview)
			}
		})
	}
}

func TestShiftTabTogglesPersistedPlanMode(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigurePlanMode(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{Tools: registry}, width: 60, height: 16, status: "ready"}
	key := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	updated, _ := m.Update(key)
	m = updated.(*model)
	if !m.planMode || !registry.PlanModeActive() || !strings.Contains(m.View().Content, " PLAN ") {
		t.Fatalf("enabled planMode=%v active=%v view=%q", m.planMode, registry.PlanModeActive(), m.View().Content)
	}
	updated, _ = m.Update(key)
	m = updated.(*model)
	if m.planMode || registry.PlanModeActive() {
		t.Fatalf("disabled planMode=%v active=%v", m.planMode, registry.PlanModeActive())
	}
}

func TestPlanModeFooterFitsNarrowViewport(t *testing.T) {
	m := &model{
		planMode: true, width: 20, height: 10, status: "review implementation plan",
		planReview: &planReviewState{event: planReviewEvent{event: tools.PlanModeEvent{PlanContent: "plan"}, reply: make(chan tools.PlanModeDecision, 1)}},
	}
	lines := strings.Split(m.View().Content, "\n")
	for _, line := range lines[len(lines)-2:] {
		if width := len([]rune(stripUIANSI(line))); width > 20 {
			t.Fatalf("footer width=%d line=%q", width, line)
		}
	}
}

func TestBridgeSerializesBlockingInteractions(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	questionDone := make(chan error, 1)
	approvalDone := make(chan error, 1)
	go func() {
		_, err := bridge.AskUserQuestion(context.Background(), tools.UserQuestionRequest{Questions: []tools.UserQuestion{{Question: "First?"}}})
		questionDone <- err
	}()
	first := (<-bridge.events).(questionEvent)
	go func() { approvalDone <- bridge.Approve(context.Background(), "shell", "go test ./...") }()
	select {
	case event := <-bridge.events:
		t.Fatalf("second interaction arrived before first resolved: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
	first.reply <- tools.UserQuestionResponse{Outcome: "cancelled"}
	if err := <-questionDone; err != nil {
		t.Fatal(err)
	}
	var approval approvalEvent
	select {
	case event := <-bridge.events:
		approval = event.(approvalEvent)
	case <-time.After(time.Second):
		t.Fatal("serialized approval did not arrive")
	}
	approval.reply <- true
	if err := <-approvalDone; err != nil {
		t.Fatal(err)
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
	if !view.AltScreen || view.MouseMode != tea.MouseModeCellMotion || view.OnMouse == nil || !strings.Contains(view.Content, "Gork Go") || !strings.Contains(view.Content, "你") {
		t.Fatalf("unexpected view: %#v", view)
	}
}

func TestMouseWheelScrollsOnlyTheTranscriptPane(t *testing.T) {
	m := &model{width: 60, height: 16}
	view := m.View()
	up := view.OnMouse(tea.MouseWheelMsg(tea.Mouse{X: 5, Y: 1, Button: tea.MouseWheelUp}))
	if up == nil {
		t.Fatal("transcript wheel-up was ignored")
	}
	updated, _ := m.Update(up())
	m = updated.(*model)
	if m.scroll != 3 {
		t.Fatalf("wheel-up scroll=%d", m.scroll)
	}
	view = m.View()
	down := view.OnMouse(tea.MouseWheelMsg(tea.Mouse{X: 5, Y: 1, Button: tea.MouseWheelDown}))
	updated, _ = m.Update(down())
	m = updated.(*model)
	if m.scroll != 0 {
		t.Fatalf("wheel-down scroll=%d", m.scroll)
	}
	view = m.View()
	if command := view.OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: 0, Button: tea.MouseWheelUp})); command != nil {
		t.Fatal("header wheel event changed transcript scroll")
	}
	if command := view.OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: m.contentHeight() + 1, Button: tea.MouseWheelUp})); command != nil {
		t.Fatal("footer wheel event changed transcript scroll")
	}
	if command := view.OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: 1, Button: tea.MouseWheelLeft})); command != nil {
		t.Fatal("horizontal wheel event changed transcript scroll")
	}
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{Y: 1, Button: tea.MouseLeft})); command != nil {
		t.Fatal("mouse click changed transcript scroll")
	}
}

func TestStreamingTextPreservesScrolledViewport(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{bridge: bridge, width: 80, height: 16, scroll: 3}
	m.transcript.WriteString("one\ntwo\nthree\nfour\nfive")
	updated, _ := m.Update(textEvent{text: "\nsix"})
	m = updated.(*model)
	if m.scroll != 4 {
		t.Fatalf("streaming scroll=%d want=4", m.scroll)
	}
	m.scroll = 0
	updated, _ = m.Update(textEvent{text: "\nseven"})
	if updated.(*model).scroll != 0 {
		t.Fatal("bottom-pinned viewport stopped following streaming text")
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

func TestMemoryFlushCommandDoesNotEnterTranscript(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, previousID: "response-1", status: "ready"}
	m.input = []rune("/flush")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.transcript.Len() != 0 || m.status != "flushing memory" {
		t.Fatalf("flush command entered normal turn: running=%v status=%q transcript=%q", m.running, m.status, m.transcript.String())
	}
}

func TestMemoryListCommandRendersWithoutModelTurn(t *testing.T) {
	store, err := memory.Open(t.TempDir(), t.TempDir(), "tui-memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write("user_requested", "## Decision\n\nList this memory."); err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{Memory: store, MemoryConfig: config}, bridge: bridge, status: "ready"}
	m.input = []rune("/memory")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.status != "listing memory" {
		t.Fatalf("memory command started a model turn: running=%v status=%q", m.running, m.status)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.running || m.status != "memory files: 1" || !strings.Contains(m.transcript.String(), "[memory] session") {
		t.Fatalf("status=%q transcript=%q", m.status, m.transcript.String())
	}
}

func TestMemoryToggleCommandDoesNotEnterModelTurn(t *testing.T) {
	store, err := memory.Open(t.TempDir(), t.TempDir(), "tui-toggle")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	runner := &agent.Runner{Memory: store, MemoryConfig: cfg}
	m := &model{ctx: context.Background(), runner: runner, bridge: bridge, status: "ready"}
	m.input = []rune("/mem off")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.status != "updating memory" || m.transcript.Len() != 0 {
		t.Fatalf("toggle entered model turn: running=%v status=%q", m.running, m.status)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.running || m.status != "Memory disabled for this session." || runner.Memory != nil {
		t.Fatalf("toggle result status=%q memory=%v", m.status, runner.Memory)
	}
}

func TestRememberReviewSelectsEnhancedAndSavesWhileMemoryDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	runner := &agent.Runner{Client: rememberTUIStreamer{}, MemoryConfig: memory.DefaultConfig()}
	m := &model{ctx: context.Background(), runner: runner, bridge: bridge, status: "ready"}
	m.input = []rune("/remember run release checks")
	updated, enhance := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if enhance == nil || m.running || m.remember == nil || m.remember.raw != "run release checks" || m.status != "enhancing memory note" {
		t.Fatalf("review=%#v running=%v status=%q", m.remember, m.running, m.status)
	}
	updated, _ = m.Update(memoryNoteEnhancedEvent{nonce: m.remember.nonce + 1, text: "stale"})
	m = updated.(*model)
	if m.remember.enhanced != "" {
		t.Fatal("stale rewrite populated review")
	}
	updated, _ = m.Update(enhance())
	m = updated.(*model)
	if m.remember.enhanced == "" || m.status != "memory note ready" {
		t.Fatalf("review=%#v status=%q", m.remember, m.status)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if !m.remember.showEnhanced {
		t.Fatal("Tab did not select enhanced note")
	}
	updated, save := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if save == nil || !m.running || m.remember != nil {
		t.Fatalf("save=%v running=%v review=%#v", save, m.running, m.remember)
	}
	updated, _ = m.Update(save())
	m = updated.(*model)
	data, err := os.ReadFile(filepath.Join(home, "memory", "MEMORY.md"))
	if err != nil || string(data) != "## Deployment\n\n- Run enhanced checks." || m.running || m.status != "memory saved" {
		t.Fatalf("data=%q running=%v status=%q err=%v", data, m.running, m.status, err)
	}
	if !strings.Contains(m.transcript.String(), "Memory saved to") {
		t.Fatalf("transcript=%q", m.transcript.String())
	}
}

func TestRememberWithoutTextEntersInputMode(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, status: "ready"}
	m.input = []rune("/remember")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.rememberInput || m.status != "remember mode" {
		t.Fatalf("command=%v mode=%v status=%q", command, m.rememberInput, m.status)
	}
	m.input = []rune("raw note")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.rememberInput || m.remember == nil || m.remember.raw != "raw note" {
		t.Fatalf("command=%v mode=%v review=%#v", command, m.rememberInput, m.remember)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.remember != nil || m.status != "memory note cancelled" {
		t.Fatalf("review=%#v status=%q", m.remember, m.status)
	}
}

func TestDreamCommandConsolidatesWithoutModelTurnUI(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	prior, err := memory.Open(root, cwd, "prior")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := prior.Write("session_end", "## Decision\n\nKeep this knowledge.")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(root, cwd, "current")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{Client: rememberTUIStreamer{}, Model: "test", Memory: store, MemoryConfig: cfg}, bridge: bridge, status: "ready"}
	m.input = []rune("/dream")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.status != "consolidating memory" || m.transcript.Len() != 0 {
		t.Fatalf("command=%v running=%v status=%q", command, m.running, m.status)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.running || m.status != "memory dream: written" {
		t.Fatalf("running=%v status=%q", m.running, m.status)
	}

	m.running = true
	m.scheduled = []tools.ScheduledTaskFired{{TaskID: "after-dream", Prompt: "continue work"}}
	updated, command = m.Update(memoryDreamDoneEvent{result: memory.DreamResult{Outcome: "written"}})
	m = updated.(*model)
	if command == nil || !m.running || m.activeTask != "after-dream" || len(m.scheduled) != 0 {
		t.Fatalf("command=%v running=%v active=%q scheduled=%#v", command, m.running, m.activeTask, m.scheduled)
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

func TestWakeCancellationRemovesPendingSyntheticTurn(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), bridge: bridge, scheduled: []tools.ScheduledTaskFired{{TaskID: "keep"}, {TaskID: "cancel"}}}
	updated, _ := m.Update(wakeCancelledEvent{id: "cancel"})
	m = updated.(*model)
	if len(m.scheduled) != 1 || m.scheduled[0].TaskID != "keep" {
		t.Fatalf("scheduled=%#v", m.scheduled)
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
