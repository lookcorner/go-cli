package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
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

func TestInputEditingSupportsCursorNavigation(t *testing.T) {
	m := &model{}
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	press(tea.Key{Code: '你', Text: "你ab"})
	press(tea.Key{Code: tea.KeyLeft})
	press(tea.Key{Code: tea.KeyLeft})
	press(tea.Key{Code: 'X', Text: "X"})
	if got := string(m.input); got != "你Xab" || m.cursor != 2 {
		t.Fatalf("middle insert=%q cursor=%d", got, m.cursor)
	}
	press(tea.Key{Code: tea.KeyDelete})
	press(tea.Key{Code: tea.KeyBackspace})
	if got := string(m.input); got != "你b" || m.cursor != 1 {
		t.Fatalf("delete/backspace=%q cursor=%d", got, m.cursor)
	}
	press(tea.Key{Code: tea.KeyHome})
	press(tea.Key{Code: tea.KeyDelete})
	press(tea.Key{Code: tea.KeyEnd})
	press(tea.Key{Code: '界', Text: "界"})
	if got := string(m.input); got != "b界" || m.cursor != 2 {
		t.Fatalf("home/end edit=%q cursor=%d", got, m.cursor)
	}
	press(tea.Key{Code: 'a', Mod: tea.ModCtrl})
	if m.cursor != 0 {
		t.Fatalf("ctrl-a cursor=%d", m.cursor)
	}
	press(tea.Key{Code: 'e', Mod: tea.ModCtrl})
	if m.cursor != len(m.input) {
		t.Fatalf("ctrl-e cursor=%d", m.cursor)
	}
	press(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	if len(m.input) != 0 || m.cursor != 0 {
		t.Fatalf("ctrl-u input=%q cursor=%d", m.input, m.cursor)
	}
}

func TestMultilineInputEnterModesAndContinuation(t *testing.T) {
	press := func(m *model, key tea.Key) (*model, tea.Cmd) {
		updated, command := m.Update(tea.KeyPressMsg(key))
		return updated.(*model), command
	}
	for _, modifier := range []tea.KeyMod{tea.ModShift, tea.ModAlt} {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}}
		m.setInput("first")
		m, _ = press(m, tea.Key{Code: tea.KeyEnter, Mod: modifier})
		m, _ = press(m, tea.Key{Code: 's', Text: "second"})
		if got := string(m.input); got != "first\nsecond" || m.running {
			t.Fatalf("modifier=%v input=%q running=%v", modifier, got, m.running)
		}
		m, command := press(m, tea.Key{Code: tea.KeyEnter})
		if command == nil || !m.running || !strings.Contains(m.transcript.String(), "first\nsecond") {
			t.Fatalf("default send modifier=%v running=%v transcript=%q", modifier, m.running, m.transcript.String())
		}
	}

	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("first")
	m, _ = press(m, tea.Key{Code: 'm', Mod: tea.ModCtrl})
	if !m.multiline {
		t.Fatal("ctrl-m did not enable multiline mode")
	}
	m, command := press(m, tea.Key{Code: tea.KeyEnter})
	if command != nil || string(m.input) != "first\n" || m.running {
		t.Fatalf("multiline newline command=%v input=%q running=%v", command != nil, m.input, m.running)
	}
	m, _ = press(m, tea.Key{Code: 's', Text: "second"})
	m, command = press(m, tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift})
	if command == nil || !m.running || !strings.Contains(m.transcript.String(), "first\nsecond") {
		t.Fatalf("multiline send running=%v transcript=%q", m.running, m.transcript.String())
	}

	m = &model{}
	m.setInput("continued\\")
	m, command = press(m, tea.Key{Code: tea.KeyEnter})
	if command != nil || string(m.input) != "continued\n" || m.cursor != len(m.input) {
		t.Fatalf("continuation command=%v input=%q cursor=%d", command != nil, m.input, m.cursor)
	}
	m.setInput("multiline\\")
	m, _ = press(m, tea.Key{Code: 'm', Mod: tea.ModCtrl})
	m, command = press(m, tea.Key{Code: tea.KeyEnter})
	if command != nil || string(m.input) != "multiline\n" {
		t.Fatalf("multiline continuation command=%v input=%q", command != nil, m.input)
	}
}

func TestMultilineCursorNavigationAndUndo(t *testing.T) {
	m := &model{}
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	m.setInput("abcd\nxy\n1234")
	m.cursor = 2
	press(tea.Key{Code: tea.KeyDown})
	if m.cursor != 7 {
		t.Fatalf("down cursor=%d", m.cursor)
	}
	press(tea.Key{Code: tea.KeyDown})
	if m.cursor != 10 {
		t.Fatalf("second down cursor=%d", m.cursor)
	}
	press(tea.Key{Code: tea.KeyUp})
	press(tea.Key{Code: tea.KeyHome})
	if m.cursor != 5 {
		t.Fatalf("line home cursor=%d", m.cursor)
	}
	press(tea.Key{Code: tea.KeyEnd})
	if m.cursor != 7 {
		t.Fatalf("line end cursor=%d", m.cursor)
	}
	press(tea.Key{Code: tea.KeyRight})
	if m.cursor != 8 {
		t.Fatalf("right across newline cursor=%d", m.cursor)
	}

	m.clearInput()
	press(tea.Key{Code: 'a', Text: "ab"})
	press(tea.Key{Code: tea.KeyBackspace})
	press(tea.Key{Code: 'z', Mod: tea.ModCtrl})
	if string(m.input) != "ab" || m.cursor != 2 {
		t.Fatalf("undo backspace input=%q cursor=%d", m.input, m.cursor)
	}
	press(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	press(tea.Key{Code: 'z', Mod: tea.ModSuper})
	if string(m.input) != "ab" || m.cursor != 2 {
		t.Fatalf("undo clear input=%q cursor=%d", m.input, m.cursor)
	}
	press(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift})
	press(tea.Key{Code: 'z', Mod: tea.ModCtrl})
	if string(m.input) != "ab" || m.cursor != 2 {
		t.Fatalf("undo newline input=%q cursor=%d", m.input, m.cursor)
	}
}

func TestInputUndoIsBounded(t *testing.T) {
	m := &model{}
	m.undoInput()
	m.insertInput("")
	if len(m.input) != 0 || len(m.inputUndo) != 0 {
		t.Fatal("empty undo or insert changed input")
	}
	for range maxInputUndoEntries + 1 {
		m.insertInput("x")
	}
	if len(m.inputUndo) != maxInputUndoEntries {
		t.Fatalf("undo entries=%d", len(m.inputUndo))
	}
	for range maxInputUndoEntries {
		m.undoInput()
	}
	if string(m.input) != "x" || m.cursor != 1 {
		t.Fatalf("bounded undo input=%q cursor=%d", m.input, m.cursor)
	}
}

func TestPromptHistoryBrowsesNewestFirstAndClosesPastNewest(t *testing.T) {
	m := &model{history: []string{"third", "second", "first"}, historyIndex: -1}
	press := func(code rune) {
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: code}))
		m = updated.(*model)
	}
	press(tea.KeyUp)
	if got := string(m.input); got != "third" || !m.historyActive {
		t.Fatalf("newest input=%q active=%v", got, m.historyActive)
	}
	press(tea.KeyUp)
	press(tea.KeyUp)
	press(tea.KeyUp)
	if got := string(m.input); got != "first" {
		t.Fatalf("oldest input=%q", got)
	}
	press(tea.KeyDown)
	press(tea.KeyDown)
	if got := string(m.input); got != "third" {
		t.Fatalf("newest again input=%q", got)
	}
	press(tea.KeyDown)
	if got := string(m.input); got != "" || m.historyActive || m.historyIndex != -1 {
		t.Fatalf("closed input=%q active=%v index=%d", got, m.historyActive, m.historyIndex)
	}
	press(tea.KeyDown)
	if got := string(m.input); got != "" || m.historyActive {
		t.Fatalf("down opened history input=%q active=%v", got, m.historyActive)
	}
	press(tea.KeyUp)
	press(tea.KeyEsc)
	if got := string(m.input); got != "" || m.historyActive {
		t.Fatalf("escape input=%q active=%v", got, m.historyActive)
	}
}

func TestPromptHistoryDraftDoesNotOpenAndTypingDetaches(t *testing.T) {
	m := &model{history: []string{"remembered"}, historyIndex: -1, input: []rune("draft"), cursor: 5}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	if got := string(m.input); got != "draft" || m.historyActive {
		t.Fatalf("draft input=%q active=%v", got, m.historyActive)
	}
	m.clearInput()
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '!', Text: "!"}))
	m = updated.(*model)
	if got := string(m.input); got != "remembered!" || m.historyActive {
		t.Fatalf("edited input=%q active=%v", got, m.historyActive)
	}
}

func TestPromptHistoryMultilineEntryKeepsBrowsing(t *testing.T) {
	m := &model{history: []string{"new\nlines", "older"}, historyIndex: -1}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	if got := string(m.input); got != "older" || !m.historyActive {
		t.Fatalf("input=%q active=%v", got, m.historyActive)
	}
}

func TestPromptHistoryLoadsWorkspaceSessionsAndDeduplicates(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	logger, err := session.NewLoggerWithID(dir, "history-tui")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": workspace})
	_ = logger.Append("user_prompt", map[string]any{"text": "  repeat  "})
	_ = logger.Append("user_prompt", map[string]any{"text": "older"})
	_ = logger.Append("user_prompt", map[string]any{"text": "repeat"})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	history := loadPromptHistory(&agent.Runner{SessionPath: logger.Path()}, workspace)
	if got := strings.Join(history, "|"); got != "repeat|older" {
		t.Fatalf("history=%q", got)
	}
}

func TestRememberPromptMovesDuplicatesToNewest(t *testing.T) {
	m := &model{history: []string{"new", "  duplicate  ", "old"}, historyIndex: 1, historyActive: true}
	m.rememberPrompt("duplicate")
	if got := strings.Join(m.history, "|"); got != "duplicate|new|old" || m.historyActive || m.historyIndex != -1 {
		t.Fatalf("history=%q active=%v index=%d", got, m.historyActive, m.historyIndex)
	}
}

func TestHistoryCommandOpensSearchWithoutStartingTurn(t *testing.T) {
	empty := &model{}
	empty.refreshHistorySearch()
	if empty.historySearch != nil {
		t.Fatal("inactive search changed")
	}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, history: []string{"newest", "oldest"}, historyIndex: -1, status: "ready"}
	m.setInput("/history")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || m.historySearch == nil {
		t.Fatalf("command=%v running=%v search=%#v", command != nil, m.running, m.historySearch)
	}
	if got := strings.Join(m.historySearch.results, "|"); got != "oldest|newest" || m.historySearch.selected != 1 || len(m.input) != 0 {
		t.Fatalf("results=%q selected=%d input=%q", got, m.historySearch.selected, m.input)
	}
}

func TestHistorySearchFiltersNavigatesAndAccepts(t *testing.T) {
	m := &model{history: []string{"generate config", "git commit", "unrelated"}, historyIndex: -1}
	m.openHistorySearch()
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	for _, char := range "gco" {
		press(tea.Key{Code: char, Text: string(char)})
	}
	if got := m.selectedHistorySearchResult(); got != "git commit" {
		t.Fatalf("best match=%q results=%q", got, m.historySearch.results)
	}
	press(tea.Key{Code: tea.KeyUp})
	press(tea.Key{Code: tea.KeyUp})
	if m.historySearch.selected != 0 {
		t.Fatalf("up wrapped selected=%d", m.historySearch.selected)
	}
	press(tea.Key{Code: tea.KeyDown})
	press(tea.Key{Code: tea.KeyDown})
	if m.historySearch.selected != len(m.historySearch.results)-1 {
		t.Fatalf("down wrapped selected=%d", m.historySearch.selected)
	}
	press(tea.Key{Code: tea.KeyTab})
	if m.historySearch != nil || string(m.input) != "git commit" || m.running {
		t.Fatalf("accepted search=%#v input=%q running=%v", m.historySearch, m.input, m.running)
	}
}

func TestHistorySearchAcceptsSelectionWithoutSubmitting(t *testing.T) {
	for _, key := range []rune{tea.KeyEnter, tea.KeyTab} {
		t.Run(tea.Key{Code: key}.String(), func(t *testing.T) {
			m := &model{history: []string{"selected"}}
			m.openHistorySearch()
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: key}))
			m = updated.(*model)
			if command != nil || m.running || m.historySearch != nil || string(m.input) != "selected" {
				t.Fatalf("command=%v running=%v search=%#v input=%q", command != nil, m.running, m.historySearch, m.input)
			}
		})
	}
}

func TestHistorySearchPagingCancellationAndNoResults(t *testing.T) {
	history := make([]string, 120)
	for index := range history {
		history[index] = fmt.Sprintf("prompt %03d", index)
	}
	m := &model{history: history, historyIndex: -1, width: 60, height: 18, scroll: 7}
	m.openHistorySearch()
	if len(m.historySearch.results) != maxHistorySearchResults || m.historySearch.selected != maxHistorySearchResults-1 || m.scroll != 0 {
		t.Fatalf("results=%d selected=%d scroll=%d", len(m.historySearch.results), m.historySearch.selected, m.scroll)
	}
	m.setInput("p")
	m.refreshHistorySearch()
	if len(m.historySearch.results) != maxHistorySearchResults {
		t.Fatalf("bounded fuzzy results=%d", len(m.historySearch.results))
	}
	m.clearInput()
	m.refreshHistorySearch()
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if m.historySearch.selected != maxHistorySearchResults-1-historySearchPageSize {
		t.Fatalf("page up selected=%d", m.historySearch.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if m.historySearch.selected != maxHistorySearchResults-1 {
		t.Fatalf("page down selected=%d", m.historySearch.selected)
	}
	view := stripUIANSI(m.View().Content)
	if !strings.Contains(view, "Prompt history") || !strings.Contains(view, "> prompt") {
		t.Fatalf("history panel not rendered:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(*model)
	if m.historySearch != nil || len(m.input) != 0 {
		t.Fatalf("cancel search=%#v input=%q", m.historySearch, m.input)
	}
	m.openHistorySearch()
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if m.historySearch != nil || len(m.input) != 0 {
		t.Fatalf("ctrl-c search=%#v input=%q", m.historySearch, m.input)
	}
	m.openHistorySearch()
	m.setInput("no possible result")
	m.refreshHistorySearch()
	if view := stripUIANSI(m.View().Content); !strings.Contains(view, "No matching prompts") || !strings.Contains(view, "no possible result") {
		t.Fatalf("no-result panel not rendered:\n%s", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.historySearch != nil || len(m.input) != 0 || m.running {
		t.Fatalf("empty accept search=%#v input=%q running=%v", m.historySearch, m.input, m.running)
	}
}

func TestStructuredInputsDoNotOpenHistorySearch(t *testing.T) {
	t.Run("question", func(t *testing.T) {
		m := &model{question: &questionState{event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{Question: "Continue?"}}}, reply: make(chan tools.UserQuestionResponse, 1)}, answers: map[string][]string{}, annotations: map[string]tools.UserQuestionAnnotation{}, partial: map[string]string{}}, history: []string{"saved"}}
		m.setInput("/history")
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if updated.(*model).historySearch != nil {
			t.Fatal("question opened history search")
		}
	})
	t.Run("plan", func(t *testing.T) {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}, planReview: &planReviewState{event: planReviewEvent{reply: make(chan tools.PlanModeDecision, 1)}, editing: true}, history: []string{"saved"}}
		m.setInput("/history")
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if updated.(*model).historySearch != nil {
			t.Fatal("plan review opened history search")
		}
	})
	t.Run("memory", func(t *testing.T) {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}, rememberInput: true, history: []string{"saved"}}
		m.setInput("/history")
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if updated.(*model).historySearch != nil {
			t.Fatal("memory input opened history search")
		}
	})
}

func TestRenderInputKeepsCursorVisibleWithinDisplayWidth(t *testing.T) {
	tests := []struct {
		input  string
		cursor int
		width  int
		want   string
	}{
		{input: "abcdef", cursor: 0, width: 5, want: "█abcd"},
		{input: "abcdef", cursor: 2, width: 5, want: "ab█cd"},
		{input: "abcdef", cursor: 6, width: 5, want: "cdef█"},
		{input: "你好ab", cursor: 1, width: 4, want: "你█"},
		{input: "abcdef", cursor: 3, width: 1, want: "█"},
	}
	for _, test := range tests {
		got := renderInput([]rune(test.input), test.cursor, test.width)
		if got != test.want || markdownVisibleWidth(got) > test.width {
			t.Fatalf("renderInput(%q,%d,%d)=%q width=%d want=%q", test.input, test.cursor, test.width, got, markdownVisibleWidth(got), test.want)
		}
	}
}

func TestRenderPromptInputShowsCursorWindowAndShrinksContent(t *testing.T) {
	input := "one\ntwo\nthree\nfour\nfive\nsix\nseven"
	lines := renderPromptInput([]rune(input), len([]rune(input)), 20, maxPromptInputRows)
	if len(lines) != maxPromptInputRows || strings.Contains(strings.Join(lines, "\n"), "one") || !strings.Contains(lines[len(lines)-1], "seven█") {
		t.Fatalf("rendered lines=%q", lines)
	}
	if got := fitInputLine([]rune("你好"), 3); got != "你" {
		t.Fatalf("wide line=%q", got)
	}
	m := &model{width: 20, height: 20}
	m.setInput(input)
	if got := m.contentHeight(); got != 10 {
		t.Fatalf("content height=%d", got)
	}
	view := stripUIANSI(m.View().Content)
	if !strings.Contains(view, "two") || !strings.Contains(view, "seven█") || strings.Contains(view, "> one") {
		t.Fatalf("multiline composer not rendered:\n%s", view)
	}
	m.height = 10
	if m.visiblePromptInputRows() != 4 || m.contentHeight() != 3 {
		t.Fatalf("small viewport rows=%d content=%d", m.visiblePromptInputRows(), m.contentHeight())
	}
}

func TestStructuredInputsShareCursorEditing(t *testing.T) {
	m := &model{planReview: &planReviewState{editing: true}, input: []rune("ab"), cursor: 2}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'X', Text: "X"}))
	m = updated.(*model)
	if got := string(m.input); got != "aXb" || m.cursor != 2 {
		t.Fatalf("plan input=%q cursor=%d", got, m.cursor)
	}
	m.planReview = nil
	m.question = &questionState{event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{Question: "Where?"}}}}}
	m.input, m.cursor = []rune("13"), 2
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '2', Text: "2"}))
	m = updated.(*model)
	if got := string(m.input); got != "123" || m.cursor != 2 {
		t.Fatalf("question input=%q cursor=%d", got, m.cursor)
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
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{Y: 1, Button: tea.MouseLeft})); command == nil {
		t.Fatal("mouse click did not start transcript selection")
	}
}

func TestTextSelectionCopiesRenderedTranscript(t *testing.T) {
	lines := []string{"alpha beta", "second 你好"}
	if got := (&textSelection{}).text(); got != "" {
		t.Fatalf("empty selection=%q", got)
	}
	if got := selectDisplayColumns("e\u0301x", 0, 0); got != "e\u0301" {
		t.Fatalf("combining selection=%q", got)
	}
	if got := selectDisplayColumns("e\u0301x", 1, 1); got != "x" {
		t.Fatalf("post-combining selection=%q", got)
	}
	if got := selectDisplayColumns("x", 2, 3); got != "" {
		t.Fatalf("out-of-range selection=%q", got)
	}
	if got := selectionPointForMouse(tea.Mouse{}, nil); got != (selectionPoint{}) {
		t.Fatalf("empty mouse point=%#v", got)
	}
	if got := selectionPointForMouse(tea.Mouse{X: 99, Y: 99}, []string{"你"}); got != (selectionPoint{column: 1}) {
		t.Fatalf("clamped mouse point=%#v", got)
	}
	blank := (&textSelection{lines: []string{""}}).highlightedLines([]string{""})
	if blank[0] != "" {
		t.Fatalf("blank highlight=%q", blank[0])
	}
	selection := textSelection{anchor: selectionPoint{line: 0, column: 6}, head: selectionPoint{line: 1, column: 5}, lines: lines, moved: true}
	if got := selection.text(); got != "beta\nsecond" {
		t.Fatalf("forward selection=%q", got)
	}
	selection.anchor, selection.head = selection.head, selection.anchor
	if got := selection.text(); got != "beta\nsecond" {
		t.Fatalf("reverse selection=%q", got)
	}
	selection.anchor, selection.head = selectionPoint{line: 1, column: 7}, selectionPoint{line: 1, column: 10}
	if got := selection.text(); got != "你好" {
		t.Fatalf("wide selection=%q", got)
	}

	m := &model{width: 60, height: 16, status: "ready"}
	m.transcript.WriteString(strings.Join(lines, "\n"))
	click := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: 6, Y: 1, Button: tea.MouseLeft}))
	updated, _ := m.Update(click())
	m = updated.(*model)
	motion := m.View().OnMouse(tea.MouseMotionMsg(tea.Mouse{X: 5, Y: 2, Button: tea.MouseLeft}))
	updated, _ = m.Update(motion())
	m = updated.(*model)
	if !strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("drag selection was not highlighted")
	}
	release := m.View().OnMouse(tea.MouseReleaseMsg(tea.Mouse{X: 5, Y: 2, Button: tea.MouseLeft}))
	updated, command := m.Update(release())
	m = updated.(*model)
	if command == nil || m.status != "selection copied" {
		t.Fatalf("release command=%v status=%q", command != nil, m.status)
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 || fmt.Sprint(batch[0]()) != "beta\nsecond" {
		t.Fatalf("clipboard batch=%#v", batch)
	}
	nonce := m.selection.nonce
	updated, _ = m.Update(selectionClearEvent{nonce: nonce})
	if updated.(*model).selection != nil {
		t.Fatal("selection highlight did not clear")
	}
}

func TestTextSelectionIgnoresClickAndClearsOnEscapeOrScroll(t *testing.T) {
	m := &model{width: 40, height: 12}
	m.transcript.WriteString("select me")
	start := func() {
		command := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseLeft}))
		updated, _ := m.Update(command())
		m = updated.(*model)
	}
	start()
	release := m.View().OnMouse(tea.MouseReleaseMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseLeft}))
	updated, command := m.Update(release())
	m = updated.(*model)
	if command != nil || m.selection != nil {
		t.Fatal("single click copied text")
	}
	start()
	firstNonce := m.selection.nonce
	start()
	updated, _ = m.Update(selectionClearEvent{nonce: firstNonce})
	m = updated.(*model)
	if m.selection == nil {
		t.Fatal("stale timer cleared a newer selection")
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.selection != nil {
		t.Fatal("escape did not clear selection")
	}
	start()
	updated, _ = m.Update(mouseScrollEvent{lines: 3})
	m = updated.(*model)
	if m.selection != nil {
		t.Fatal("scroll did not clear selection")
	}
	start()
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if updated.(*model).selection != nil {
		t.Fatal("keyboard scroll did not clear selection")
	}
}

func TestMouseClickAnswersApproval(t *testing.T) {
	for _, test := range []struct {
		name    string
		x       int
		allowed bool
	}{
		{name: "approve", x: 1, allowed: true},
		{name: "deny", x: 12, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reply := make(chan bool, 1)
			m := &model{width: 60, height: 16, approval: &approvalEvent{reply: reply}}
			view := m.View()
			command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: test.x, Y: m.contentHeight() + 3, Button: tea.MouseLeft}))
			if command == nil {
				t.Fatal("approval click was ignored")
			}
			updated, _ := m.Update(command())
			m = updated.(*model)
			if got := <-reply; got != test.allowed || m.approval != nil {
				t.Fatalf("allowed=%v approval=%#v", got, m.approval)
			}
		})
	}
}

func TestMouseClickAnswersPlanReview(t *testing.T) {
	for _, test := range []struct {
		name string
		x    int
		want string
		edit bool
	}{
		{name: "approve", x: 1, want: "approved"},
		{name: "revise", x: 14, edit: true},
		{name: "abandon", x: 36, want: "abandoned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reply := make(chan tools.PlanModeDecision, 1)
			m := &model{width: 60, height: 16, planReview: &planReviewState{event: planReviewEvent{reply: reply}}}
			view := m.View()
			command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: test.x, Y: m.contentHeight() + 3, Button: tea.MouseLeft}))
			if command == nil {
				t.Fatal("plan review click was ignored")
			}
			updated, _ := m.Update(command())
			m = updated.(*model)
			if test.edit {
				if m.planReview == nil || !m.planReview.editing {
					t.Fatalf("plan review=%#v", m.planReview)
				}
				return
			}
			if decision := <-reply; decision.Outcome != test.want || m.planReview != nil {
				t.Fatalf("decision=%#v review=%#v", decision, m.planReview)
			}
		})
	}
}

func TestMouseClickSelectsAndDoubleClicksQuestionOptions(t *testing.T) {
	reply := make(chan tools.UserQuestionResponse, 1)
	m := &model{width: 60, height: 16, question: &questionState{
		event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
			Question: "Deploy where?", Options: []tools.UserQuestionOption{{Label: "Local"}, {Label: "Cloud"}},
		}}}, reply: reply},
		answers: make(map[string][]string), annotations: make(map[string]tools.UserQuestionAnnotation), partial: make(map[string]string),
	}}
	click := func(x int) {
		command := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: x, Y: m.contentHeight() + 3, Button: tea.MouseLeft}))
		if command == nil {
			t.Fatal("question option click was ignored")
		}
		updated, _ := m.Update(command())
		m = updated.(*model)
	}
	click(12)
	if got := string(m.input); got != "2" {
		t.Fatalf("input=%q", got)
	}
	click(12)
	select {
	case response := <-reply:
		if response.Outcome != "accepted" || response.Answers["Deploy where?"][0] != "Cloud" {
			t.Fatalf("response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("double-clicked question did not complete")
	}
}

func TestMouseClickTogglesMultiSelectQuestionOptions(t *testing.T) {
	m := &model{width: 60, height: 16, question: &questionState{event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
		Question: "Targets?", MultiSelect: true, Options: []tools.UserQuestionOption{{Label: "API"}, {Label: "UI"}},
	}}}}}}
	for _, x := range []int{1, 9} {
		command := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: x, Y: m.contentHeight() + 3, Button: tea.MouseLeft}))
		updated, _ := m.Update(command())
		m = updated.(*model)
	}
	if got := string(m.input); got != "1, 2" {
		t.Fatalf("input=%q", got)
	}
	m.questionClick.at = time.Now().Add(-questionDoubleClickWindow)
	command := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: m.contentHeight() + 3, Button: tea.MouseLeft}))
	updated, _ := m.Update(command())
	m = updated.(*model)
	if got := string(m.input); got != "2" {
		t.Fatalf("toggled input=%q", got)
	}
}

func TestQuestionOptionSelectionCanBeUndone(t *testing.T) {
	m := &model{question: &questionState{event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
		Question: "Deploy where?", Options: []tools.UserQuestionOption{{Label: "Local"}, {Label: "Cloud"}},
	}}}}}}
	m.input = []rune("custom")
	m.cursor = 3
	m.selectQuestionOption(1, true)
	if got := string(m.input); got != "2" {
		t.Fatalf("selected input=%q", got)
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'z', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if got := string(m.input); got != "custom" || m.cursor != 3 {
		t.Fatalf("undo input=%q cursor=%d", got, m.cursor)
	}
}

func TestMouseClickIgnoresFooterGapsAndClippedActions(t *testing.T) {
	m := &model{width: 60, height: 16, approval: &approvalEvent{reply: make(chan bool, 1)}}
	view := m.View()
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 9, Y: m.contentHeight() + 3, Button: tea.MouseLeft})); command != nil {
		t.Fatal("approval gap was clickable")
	}
	m.approval = nil
	m.planReview = &planReviewState{event: planReviewEvent{reply: make(chan tools.PlanModeDecision, 1)}}
	view = m.View()
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 12, Y: m.contentHeight() + 3, Button: tea.MouseLeft})); command != nil {
		t.Fatal("plan action gap was clickable")
	}
	m.width = 20
	view = m.View()
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 16, Y: m.contentHeight() + 3, Button: tea.MouseLeft})); command != nil {
		t.Fatal("clipped plan action was clickable")
	}
	m.planReview = nil
	m.width = 20
	m.question = &questionState{event: questionEvent{request: tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
		Question: "Pick?", Options: []tools.UserQuestionOption{{Label: "Visible"}, {Label: "Clipped option"}},
	}}}}}
	view = m.View()
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 15, Y: m.contentHeight() + 3, Button: tea.MouseLeft})); command != nil {
		t.Fatal("clipped question option was clickable")
	}
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: m.contentHeight() + 2, Button: tea.MouseLeft})); command != nil {
		t.Fatal("question title was clickable")
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
