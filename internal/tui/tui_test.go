package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

type recapTUIStreamer struct{ request api.ResponseRequest }
type promptSuggestionTUIStreamer struct{ request api.ResponseRequest }
type modelTUIStreamer struct{ history []session.Message }

func (s *modelTUIStreamer) StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error) {
	return api.StreamResult{ResponseID: "new-response", Text: "done"}, nil
}

func (s *modelTUIStreamer) RewindHistory(messages []session.Message) {
	s.history = append([]session.Message(nil), messages...)
}

func (s *promptSuggestionTUIStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "run the complete test suite"}, nil
}

func (s *recapTUIStreamer) CloneForCompaction(bool) api.Streamer { return s }

func (s *recapTUIStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "We fixed task rendering in internal/tui."}, nil
}

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

func TestBridgeUsesConfiguredPromptApprover(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	bridge.SetPromptApprover(tools.PromptApprover{Mode: tools.PermissionDeny})
	if err := bridge.Approve(context.Background(), "shell", "git push origin main"); err == nil || !tools.IsPermissionDenied(err) {
		t.Fatalf("configured prompt approver error=%v", err)
	}
	select {
	case event := <-bridge.events:
		t.Fatalf("bridge prompt bypassed configured approver: %#v", event)
	default:
	}
}

func TestAlwaysApproveCommandTogglesBridgeMode(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	m := &model{bridge: bridge, width: 60, height: 16, status: "ready"}
	m.setInput("/always-approve ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || bridge.PermissionMode() != tools.PermissionAlwaysApprove || m.status != "always-approve mode" || !strings.Contains(m.View().Content, " ALWAYS ") {
		t.Fatalf("enable command=%v running=%v mode=%q status=%q", command != nil, m.running, bridge.PermissionMode(), m.status)
	}
	if err := bridge.Approve(context.Background(), "shell", "go test ./..."); err != nil {
		t.Fatalf("automatic approval failed: %v", err)
	}
	asked := make(chan error, 1)
	go func() { asked <- PromptApprover(bridge).Approve(context.Background(), "shell", "git push") }()
	event := (<-bridge.events).(approvalEvent)
	event.reply <- true
	if err := <-asked; err != nil {
		t.Fatalf("explicit ask was not preserved: %v", err)
	}
	m.setInput("/always-approve")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || bridge.PermissionMode() != tools.PermissionPrompt || m.status != "normal mode" || strings.Contains(m.View().Content, " ALWAYS ") {
		t.Fatalf("disable command=%v mode=%q status=%q", command != nil, bridge.PermissionMode(), m.status)
	}

	denied := NewBridge(context.Background(), tools.PermissionDeny)
	defer denied.Close()
	m = &model{bridge: denied}
	m.setInput("/always-approve")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if denied.PermissionMode() != tools.PermissionDeny || !strings.Contains(m.status, "disabled by deny mode") {
		t.Fatalf("deny mode=%q status=%q", denied.PermissionMode(), m.status)
	}
}

func TestAutoCommandTogglesBridgeMode(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	var persisted []string
	bridge.SetPermissionModePersister(func(mode string) error {
		persisted = append(persisted, mode)
		return nil
	})
	policy, err := tools.NewPolicyApprover(bridge, PromptApprover(bridge), nil, []string{"Bash(git push *)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &model{bridge: bridge, width: 60, height: 16, status: "ready"}
	for _, test := range []struct {
		command string
		want    tools.PermissionMode
		status  string
	}{
		{command: "/auto ignored", want: tools.PermissionAuto, status: "auto permission mode"},
		{command: "/auto", want: tools.PermissionPrompt, status: "normal mode"},
		{command: "/always-approve", want: tools.PermissionAlwaysApprove, status: "always-approve mode"},
		{command: "/auto", want: tools.PermissionAuto, status: "auto permission mode"},
	} {
		m.setInput(test.command)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || bridge.PermissionMode() != test.want || m.status != test.status {
			t.Fatalf("input=%q command=%v mode=%q status=%q", test.command, command != nil, bridge.PermissionMode(), m.status)
		}
	}
	if !strings.Contains(m.View().Content, " AUTO ") || strings.Contains(m.View().Content, " ALWAYS ") {
		t.Fatalf("mode badges=%q", m.View().Content)
	}
	if got := strings.Join(persisted, ","); got != "auto,ask,always-approve,auto" {
		t.Fatalf("persisted modes=%q", got)
	}
	if err := policy.Approve(context.Background(), "shell", "go test ./..."); err != nil {
		t.Fatalf("auto mode did not reach tool policy: %v", err)
	}
	asked := make(chan error, 1)
	go func() { asked <- policy.Approve(context.Background(), "shell", "git push origin main") }()
	event := (<-bridge.events).(approvalEvent)
	event.reply <- true
	if err := <-asked; err != nil {
		t.Fatalf("explicit ask rule was not preserved: %v", err)
	}
}

func TestAutoCommandHonorsFeatureAndDenyGates(t *testing.T) {
	for _, test := range []struct {
		name   string
		bridge *Bridge
	}{
		{name: "feature disabled", bridge: NewBridgeWithLocks(context.Background(), tools.PermissionAuto, false, true)},
		{name: "deny mode", bridge: NewBridgeWithLocks(context.Background(), tools.PermissionDeny, false, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer test.bridge.Close()
			writes := 0
			test.bridge.SetPermissionModePersister(func(string) error { writes++; return nil })
			m := &model{bridge: test.bridge, width: 60, height: 16}
			m.setInput("/help")
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if strings.Contains(m.transcript.String(), "`/auto`") {
				t.Fatalf("disabled command shown in help: %q", m.transcript.String())
			}
			m.setInput("/auto")
			updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if !strings.Contains(m.status, "disabled") || test.bridge.PermissionMode() == tools.PermissionAuto {
				t.Fatalf("mode=%q status=%q", test.bridge.PermissionMode(), m.status)
			}
			if err := test.bridge.SetAutoMode(true); err == nil || !strings.Contains(err.Error(), "disabled") {
				t.Fatalf("direct auto enable error=%v", err)
			}
			if writes != 0 {
				t.Fatalf("disabled mode persisted %d writes", writes)
			}
		})
	}
}

func TestPermissionModePersistenceFailureRollsBack(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	bridge.SetPermissionModePersister(func(string) error { return errors.New("disk full") })
	m := &model{bridge: bridge, width: 60, height: 16}
	for _, command := range []string{"/auto", "/always-approve"} {
		m.setInput(command)
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if bridge.PermissionMode() != tools.PermissionPrompt || !strings.Contains(m.status, "persist permission mode: disk full") || strings.Contains(m.View().Content, " AUTO ") || strings.Contains(m.View().Content, " ALWAYS ") {
			t.Fatalf("command=%q mode=%q status=%q", command, bridge.PermissionMode(), m.status)
		}
	}
}

func TestBridgeManagedAutoLock(t *testing.T) {
	bridge := NewBridgeWithAutoLock(context.Background(), tools.PermissionAlwaysApprove, true)
	defer bridge.Close()
	if bridge.PermissionMode() != tools.PermissionPrompt {
		t.Fatalf("initial mode=%q", bridge.PermissionMode())
	}
	if err := bridge.SetAlwaysApprove(true); err == nil || bridge.PermissionMode() != tools.PermissionPrompt {
		t.Fatalf("enable err=%v mode=%q", err, bridge.PermissionMode())
	}
	if err := bridge.SetAlwaysApprove(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	auto := NewBridgeWithAutoLock(context.Background(), tools.PermissionAuto, true)
	defer auto.Close()
	if auto.PermissionMode() != tools.PermissionAuto || !strings.Contains((&model{bridge: auto, width: 60, height: 16}).View().Content, " AUTO ") {
		t.Fatalf("classifier auto mode=%q", auto.PermissionMode())
	}
}

func TestBridgeAutoModePromptsOnlyForRisk(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	if err := bridge.Approve(context.Background(), "shell", "go test ./..."); err != nil {
		t.Fatalf("routine command: %v", err)
	}
	select {
	case event := <-bridge.events:
		t.Fatalf("routine command prompted: %#v", event)
	default:
	}
	result := make(chan error, 1)
	go func() { result <- bridge.Approve(context.Background(), "shell", "git push origin main") }()
	event := (<-bridge.events).(approvalEvent)
	event.reply <- true
	if err := <-result; err != nil {
		t.Fatalf("risky command approval: %v", err)
	}
	if err := bridge.Approve(tools.WithPermissionBypass(context.Background()), "shell", "git push origin main"); err != nil {
		t.Fatalf("bypassed command: %v", err)
	}
	select {
	case event := <-bridge.events:
		t.Fatalf("bypassed command prompted: %#v", event)
	default:
	}
	classified := tools.WithPermissionClassifier(context.Background(), func(context.Context, string, string) (bool, error) {
		return true, nil
	})
	if err := bridge.Approve(classified, "shell", "touch classified.txt"); err != nil {
		t.Fatalf("classified command: %v", err)
	}
	select {
	case event := <-bridge.events:
		t.Fatalf("classified command prompted: %#v", event)
	default:
	}
}

func TestBridgeQuestionSelectionAndPlanClarification(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, bridge: bridge, width: 70, height: 18, running: true, mouseToggle: true}
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
	if m.question != nil || m.status != "thinking" || m.mouseReleased {
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

func TestPlanSlashCommandEntersModeAndStartsDescription(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{Tools: registry}, status: "ready"}
	m.setInput("/plan")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || !m.planMode || !registry.PlanModeActive() || m.status != "plan mode" {
		t.Fatalf("plain command=%v running=%v plan=%v active=%v status=%q", command != nil, m.running, m.planMode, registry.PlanModeActive(), m.status)
	}
	m.setInput("/plan Refactor the auth flow")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || !m.planMode || !strings.Contains(m.transcript.String(), "Refactor the auth flow") {
		t.Fatalf("description command=%v running=%v plan=%v transcript=%q", command != nil, m.running, m.planMode, m.transcript.String())
	}
}

func TestViewPlanCommandsOpenReadOnlyPreview(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(".grok/plan.md", "# Plan\n\n1. Refactor auth.\n", true); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	for _, prompt := range []string{"/view-plan ignored", "/show-plan", "/plan-view"} {
		m := &model{ctx: context.Background(), runner: &agent.Runner{Tools: registry}, width: 60, height: 16}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.running || m.viewer == nil || m.status != "plan preview" || !strings.Contains(m.View().Content, "Refactor auth") {
			t.Fatalf("prompt=%q command=%v running=%v preview=%v status=%q view=%q", prompt, command != nil, m.running, m.viewer != nil, m.status, m.View().Content)
		}
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
		m = updated.(*model)
		if m.viewer != nil || m.status != "ready" {
			t.Fatalf("preview did not close: preview=%v status=%q", m.viewer != nil, m.status)
		}
	}
}

func TestTranscriptCommandsUseCompletedSession(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "transcript-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.AppendPrompt("Inspect the auth flow", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "Auth is configured.", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"/transcript ignored", "/log"} {
		m := &model{ctx: context.Background(), runner: &agent.Runner{SessionPath: logger.Path()}, width: 60, height: 16}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		view := m.View().Content
		if command != nil || m.running || m.viewer == nil || m.status != "transcript" || !strings.Contains(view, "Inspect the auth flow") || !strings.Contains(view, "Auth is configured") {
			t.Fatalf("prompt=%q command=%v running=%v viewer=%v status=%q view=%q", prompt, command != nil, m.running, m.viewer != nil, m.status, view)
		}
	}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/transcript")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.viewer != nil || m.status != "no active session to view" {
		t.Fatalf("missing session command=%v viewer=%v status=%q", command != nil, m.viewer != nil, m.status)
	}
}

func TestRenameCommandsPersistWithoutModelTurn(t *testing.T) {
	for _, prompt := range []string{"/rename Release work", "/title Release work"} {
		logger, err := session.NewLoggerWithID(t.TempDir(), "rename-session")
		if err != nil {
			t.Fatal(err)
		}
		m := &model{ctx: context.Background(), runner: &agent.Runner{Logger: logger, SessionID: logger.ID()}}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.running || !strings.Contains(m.status, "Release work") {
			t.Fatalf("prompt=%q command=%v running=%v status=%q", prompt, command != nil, m.running, m.status)
		}
		info, err := session.InfoByID(filepath.Dir(logger.Path()), logger.ID())
		if err != nil || info.Title != "Release work" {
			t.Fatalf("prompt=%q title=%q err=%v", prompt, info.Title, err)
		}
		logger.Close()
	}
}

func TestExportCommandCopiesOrWritesWithoutModelTurn(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "export-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.AppendPrompt("Inspect auth", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "Auth is ready.", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	m := &model{ctx: context.Background(), runner: &agent.Runner{SessionPath: logger.Path()}, workspace: root}
	m.setInput("/export")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, clipboard := m.Update(command())
	m = updated.(*model)
	if clipboard == nil || m.running || m.status != "conversation copied to clipboard" {
		t.Fatalf("clipboard=%v running=%v status=%q", clipboard != nil, m.running, m.status)
	}
	m.setInput("/export exports/my conversation.md")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, clipboard = m.Update(command())
	m = updated.(*model)
	path := filepath.Join(root, "exports", "my conversation.md")
	if clipboard != nil || m.running || !strings.Contains(m.status, path) {
		t.Fatalf("clipboard=%v running=%v status=%q", clipboard != nil, m.running, m.status)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "Auth is ready") {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestExitSlashCommandsQuitWithoutModelTurn(t *testing.T) {
	for _, prompt := range []string{"/quit ignored", "/exit"} {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command == nil || m.running || m.transcript.Len() != 0 {
			t.Fatalf("prompt=%q command=%v running=%v transcript=%q", prompt, command != nil, m.running, m.transcript.String())
		}
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

func TestBusyModelQueuesPromptsAndShowsQueueWithoutModelTurn(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, running: true, width: 70, height: 18, status: "thinking"}
	m.setInput("first follow-up")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.running || len(m.pendingPrompts) != 1 || m.pendingPrompts[0] != "first follow-up" || m.status != "queued prompt #1" {
		t.Fatalf("command=%v running=%v queue=%#v status=%q", command != nil, m.running, m.pendingPrompts, m.status)
	}
	m.setInput("second line\ncontinued")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || len(m.pendingPrompts) != 2 {
		t.Fatalf("command=%v queue=%#v", command != nil, m.pendingPrompts)
	}
	m.setInput("/queue ignored")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	queue := m.transcript.String()
	if command != nil || len(m.pendingPrompts) != 2 || !strings.Contains(queue, "Queued prompts (2):") || !strings.Contains(queue, "#1  first follow-up") || !strings.Contains(queue, "#2  second line  (+1 more line)") {
		t.Fatalf("command=%v queue=%#v transcript=%q", command != nil, m.pendingPrompts, queue)
	}
	m.setInput("/compact")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || string(m.input) != "/compact" || len(m.pendingPrompts) != 2 || !strings.Contains(m.status, "only prompts") {
		t.Fatalf("command=%v input=%q queue=%#v status=%q", command != nil, m.input, m.pendingPrompts, m.status)
	}
}

func TestQueuedPromptsRunFIFOBeforeScheduledWake(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &scheduledTUIStreamer{}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test"},
		pendingPrompts: []string{"first follow-up", "second follow-up"}, scheduled: []tools.ScheduledTaskFired{{TaskID: "loop-1", Prompt: "scheduled"}},
	}
	updated, command := m.Update(turnDoneEvent{result: agent.Result{ResponseID: "first-response"}})
	m = updated.(*model)
	if command == nil || !m.running || len(m.pendingPrompts) != 1 || len(m.scheduled) != 1 || m.activeTask != "" {
		t.Fatalf("command=%v running=%v pending=%#v scheduled=%#v active=%q", command != nil, m.running, m.pendingPrompts, m.scheduled, m.activeTask)
	}
	first := command()
	if _, ok := first.(turnDoneEvent); !ok || streamer.request.PreviousResponseID != "first-response" || streamer.request.Input[0].Content != "first follow-up" {
		t.Fatalf("first=%#v request=%#v", first, streamer.request)
	}
	updated, command = m.Update(first)
	m = updated.(*model)
	if command == nil {
		t.Fatal("second queued prompt did not start")
	}
	second := command()
	if streamer.request.PreviousResponseID != "scheduled-response" || streamer.request.Input[0].Content != "second follow-up" {
		t.Fatalf("second=%#v request=%#v", second, streamer.request)
	}
	updated, command = m.Update(second)
	m = updated.(*model)
	if command == nil {
		t.Fatal("scheduled wake did not start after queued prompts")
	}
	scheduled := command()
	if m.activeTask != "loop-1" || len(m.scheduled) != 0 || streamer.request.PreviousResponseID != "scheduled-response" || streamer.request.Input[0].Content != "scheduled" {
		t.Fatalf("scheduled=%#v request=%#v active=%q remaining=%#v", scheduled, streamer.request, m.activeTask, m.scheduled)
	}
}

func TestQueueCommandIsInstantWhenIdle(t *testing.T) {
	m := &model{runner: &agent.Runner{}, pendingPrompts: []string{"follow-up"}}
	m.setInput("/queue ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || !strings.Contains(m.transcript.String(), "Queued prompt (1):") {
		t.Fatalf("command=%v running=%v transcript=%q", command != nil, m.running, m.transcript.String())
	}
}

func TestFormatPromptQueueEmpty(t *testing.T) {
	if got := formatPromptQueue(nil); got != "Queue is empty." {
		t.Fatalf("queue=%q", got)
	}
}

func TestTasksCommandShowsAllTaskSourcesWithoutModelTurn(t *testing.T) {
	exitCode := 1
	end := tools.ProcessTime{SecsSinceEpoch: 13}
	runner := &agent.Runner{
		SessionID: "session-1",
		ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{
				{ID: "done-sub", Type: "explore", Status: "completed", Description: "old", StartedAtMS: 1, DurationMS: 2100},
				{ID: "run-sub", Type: "worker", Status: "running", Description: "build feature", StartedAtMS: 2, DurationMS: 1500},
			}
		},
		ListTasks: func() []tools.ProcessSnapshot {
			return []tools.ProcessSnapshot{{
				TaskID: "task-1", Command: "go test ./...", Description: "\n run full tests\n", StartTime: tools.ProcessTime{SecsSinceEpoch: 10}, EndTime: &end,
				Completed: true, ExitCode: &exitCode,
			}}
		},
	}
	snapshot := runner.TaskSnapshot()
	snapshot.Scheduled = []tools.ScheduledTaskCreated{{TaskID: "loop-1", Prompt: "check deploy", HumanSchedule: "every 5 minutes"}}
	text := formatTaskSnapshot(snapshot, time.Unix(20, 0))
	if !strings.Contains(text, "Tasks (4):") || !strings.Contains(text, "running   worker · build feature") || !strings.Contains(text, "completed explore · old") || !strings.Contains(text, "failed    Task · run full tests") || !strings.Contains(text, "scheduled every 5 minutes · check deploy") {
		t.Fatalf("tasks=%q", text)
	}

	m := &model{runner: runner}
	m.setInput("/tasks ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || m.status != "tasks" || !strings.Contains(m.transcript.String(), "Tasks (3):") {
		t.Fatalf("command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
	}
	m.running = true
	m.setInput("/tasks")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.running || m.status != "tasks" || len(m.pendingPrompts) != 0 || string(m.input) != "" {
		t.Fatalf("busy command=%v running=%v status=%q queue=%#v input=%q", command != nil, m.running, m.status, m.pendingPrompts, m.input)
	}
}

func TestTasksCommandRequiresSessionAndFormatsEmptyState(t *testing.T) {
	m := &model{runner: &agent.Runner{}}
	m.setInput("/tasks")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "no active session" || m.transcript.Len() != 0 {
		t.Fatalf("command=%v status=%q transcript=%q", command != nil, m.status, m.transcript.String())
	}
	if got := formatTaskSnapshot(agent.TaskSnapshot{}, time.Now()); got != "No background tasks or subagents." {
		t.Fatalf("empty tasks=%q", got)
	}
}

func TestRecapCommandIsDisplayOnlyWhenIdle(t *testing.T) {
	streamer := &recapTUIStreamer{}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, SessionID: "session-1", Model: "test"},
		previousID: "response-1", status: "ready",
	}
	m.setInput("/recap ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.running || !m.recapRunning || m.status != "generating recap" {
		t.Fatalf("command=%v running=%v recap=%v status=%q", command != nil, m.running, m.recapRunning, m.status)
	}
	updated, followup := m.Update(command())
	m = updated.(*model)
	if followup != nil || m.recapRunning || m.previousID != "response-1" || m.status != "recap" || !strings.Contains(m.transcript.String(), "Recap \u2014 We fixed task rendering") {
		t.Fatalf("followup=%v recap=%v previous=%q status=%q transcript=%q", followup != nil, m.recapRunning, m.previousID, m.status, m.transcript.String())
	}
	if len(streamer.request.Tools) != 0 || streamer.request.PreviousResponseID != "response-1" {
		t.Fatalf("request=%#v", streamer.request)
	}
}

func TestRecapCommandBypassesBusyPromptQueue(t *testing.T) {
	streamer := &recapTUIStreamer{}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, SessionID: "session-1"},
		previousID: "response-1", running: true, status: "thinking",
	}
	m.setInput("/recap")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || !m.recapRunning || len(m.pendingPrompts) != 0 || string(m.input) != "" || m.status != "thinking" {
		t.Fatalf("command=%v running=%v recap=%v queue=%#v input=%q status=%q", command != nil, m.running, m.recapRunning, m.pendingPrompts, m.input, m.status)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.pendingRecap == "" || strings.Contains(m.transcript.String(), recapLabel) {
		t.Fatalf("pending=%q transcript=%q", m.pendingRecap, m.transcript.String())
	}
	updated, _ = m.Update(turnDoneEvent{result: agent.Result{ResponseID: "response-2", Steps: 1}})
	m = updated.(*model)
	if m.pendingRecap != "" || m.previousID != "response-2" || !strings.Contains(m.transcript.String(), "Recap \u2014 We fixed task rendering") {
		t.Fatalf("pending=%q previous=%q transcript=%q", m.pendingRecap, m.previousID, m.transcript.String())
	}
}

func TestRecapCommandRequiresSessionAndDropsStaleResult(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/recap")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "no active session" {
		t.Fatalf("command=%v status=%q", command != nil, m.status)
	}
	m.running, m.recapRunning, m.promptSerial = true, true, 2
	m.setInput("new queued work")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.promptSerial != 3 || len(m.pendingPrompts) != 1 {
		t.Fatalf("command=%v serial=%d queue=%#v", command != nil, m.promptSerial, m.pendingPrompts)
	}
	updated, _ = m.Update(recapDoneEvent{text: "stale", serial: 2})
	m = updated.(*model)
	if m.recapRunning || m.transcript.Len() != 0 {
		t.Fatalf("recap=%v transcript=%q", m.recapRunning, m.transcript.String())
	}
}

func TestBtwCommandShowsDisplayOnlyAnswer(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &recapTUIStreamer{}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, SessionID: "session-1", SessionPath: logger.Path()},
		previousID: "response-1", status: "ready", width: 60, height: 16,
	}
	m.setInput("/btw what changed?")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.running || !m.btwRunning || m.status != "asking side question" {
		t.Fatalf("command=%v running=%v btw=%v status=%q", command != nil, m.running, m.btwRunning, m.status)
	}
	updated, followup := m.Update(command())
	m = updated.(*model)
	if followup != nil || m.btwRunning || m.viewer == nil || m.viewer.title != "Side question" || m.previousID != "response-1" || m.transcript.Len() != 0 || !strings.Contains(m.View().Content, "what changed?") || !strings.Contains(m.View().Content, "We fixed task rendering") {
		t.Fatalf("followup=%v btw=%v viewer=%#v previous=%q transcript=%q view=%q", followup != nil, m.btwRunning, m.viewer, m.previousID, m.transcript.String(), m.View().Content)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.viewer != nil || m.status != "ready" {
		t.Fatalf("viewer=%#v status=%q", m.viewer, m.status)
	}
}

func TestBtwCommandBypassesBusyPromptQueue(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: &recapTUIStreamer{}, SessionID: "session-1", SessionPath: logger.Path()},
		previousID: "response-1", running: true, status: "thinking",
	}
	m.setInput("/btw status?")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || !m.btwRunning || len(m.pendingPrompts) != 0 || m.status != "thinking" {
		t.Fatalf("command=%v running=%v btw=%v queue=%#v status=%q", command != nil, m.running, m.btwRunning, m.pendingPrompts, m.status)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.viewer == nil || !m.running || len(m.pendingPrompts) != 0 {
		t.Fatalf("viewer=%#v running=%v queue=%#v", m.viewer, m.running, m.pendingPrompts)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.viewer != nil || m.status != "thinking" {
		t.Fatalf("viewer=%#v status=%q", m.viewer, m.status)
	}
}

func TestBtwCommandValidatesSessionQuestionAndScrollsViewer(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/btw question")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "no active session" {
		t.Fatalf("command=%v status=%q", command != nil, m.status)
	}
	m.runner.SessionID, m.runner.SessionPath = "session-1", "/tmp/session-1.jsonl"
	m.setInput("/btw")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "usage: /btw <question>" {
		t.Fatalf("command=%v status=%q", command != nil, m.status)
	}
	m.width, m.height = 30, 10
	m.viewer = &readOnlyViewer{title: "Side question", content: strings.Repeat("long answer line\n", 30)}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	if m.scroll == 0 || m.scroll > m.maxViewerScroll() {
		t.Fatalf("scroll=%d max=%d", m.scroll, m.maxViewerScroll())
	}
}

func TestModelShellCommandDoesNotStartModelTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	m := &model{ctx: context.Background(), runner: &agent.Runner{Tools: registry}, bridge: bridge, width: 60, height: 16, status: "ready"}
	m.setInput("! printf shell-output")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.previousID != "" {
		t.Fatalf("shell did not start correctly: running=%v previous=%q command=%v", m.running, m.previousID, command != nil)
	}
	message := command()
	updated, _ = m.Update(message)
	m = updated.(*model)
	if m.running || m.status != "shell completed" || !strings.Contains(m.transcript.String(), "shell-output") {
		t.Fatalf("shell result not rendered: running=%v status=%q transcript=%q", m.running, m.status, m.transcript.String())
	}
}

func TestMouseReportingToggle(t *testing.T) {
	m := &model{width: 60, height: 16, status: "ready", mouseToggle: true}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse reporting did not start captured")
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || m.mouseReleased || m.status != "ready" {
		t.Fatalf("prompt shortcut changed mouse state=%#v command=%v", m, command != nil)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || !m.mouseReleased || m.View().MouseMode != tea.MouseModeNone || m.status != "mouse reporting disabled" {
		t.Fatalf("disabled mouse state=%#v mode=%v command=%v", m, m.View().MouseMode, command != nil)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if m.mouseReleased || m.View().MouseMode != tea.MouseModeCellMotion || m.status != "mouse reporting enabled" {
		t.Fatalf("enabled mouse state=%#v mode=%v", m, m.View().MouseMode)
	}

	m.scrollFocused = false
	m.selection = &textSelection{}
	m.selectionClick = selectionClickState{count: 2}
	m.setInput("/toggle-mouse-reporting ignored")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.mouseReleased || m.View().MouseMode != tea.MouseModeNone || m.status != "mouse reporting disabled" {
		t.Fatalf("command did not disable mouse state=%#v mode=%v command=%v", m, m.View().MouseMode, command != nil)
	}
	if m.selection != nil || m.selectionClick != (selectionClickState{}) {
		t.Fatalf("command did not clear selection state: selection=%#v click=%#v", m.selection, m.selectionClick)
	}
	m.setInput("/toggle-mouse-reporting")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.mouseReleased || m.View().MouseMode != tea.MouseModeCellMotion || m.status != "mouse reporting enabled" {
		t.Fatalf("command did not enable mouse state=%#v mode=%v", m, m.View().MouseMode)
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "`/toggle-mouse-reporting`") {
		t.Fatalf("enabled help omitted mouse command: %q", m.transcript.String())
	}

	disabled := &model{width: 60, height: 16, status: "ready", scrollFocused: true}
	updated, _ = disabled.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	disabled = updated.(*model)
	if disabled.mouseReleased || disabled.status != "ready" {
		t.Fatalf("disabled shortcut changed state=%#v", disabled)
	}
	disabled.scrollFocused = false
	disabled.setInput("/toggle-mouse-reporting ignored")
	updated, command = disabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	disabled = updated.(*model)
	if command != nil || disabled.mouseReleased || disabled.View().MouseMode != tea.MouseModeCellMotion || disabled.status != "mouse reporting toggle is off" {
		t.Fatalf("disabled command changed state=%#v mode=%v command=%v", disabled, disabled.View().MouseMode, command != nil)
	}
	if !strings.Contains(disabled.transcript.String(), "Mouse reporting toggle is off. Set `[ui] mouse_reporting_toggle = true` in ~/.grok/config.toml to enable it.") {
		t.Fatalf("disabled command omitted configuration hint: %q", disabled.transcript.String())
	}
	disabled.setInput("/help")
	updated, _ = disabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	disabled = updated.(*model)
	if strings.Contains(disabled.transcript.String(), "`/toggle-mouse-reporting`") {
		t.Fatalf("disabled help exposed mouse command: %q", disabled.transcript.String())
	}
}

func TestVimModeCommandPersistsAndChangesNavigation(t *testing.T) {
	var persisted []bool
	m := &model{
		runner: &agent.Runner{}, width: 80, height: 12, status: "ready", historyIndex: -1,
		persistVimMode: func(enabled bool) error {
			persisted = append(persisted, enabled)
			return nil
		},
	}
	for line := 0; line < 40; line++ {
		fmt.Fprintf(&m.transcript, "line %02d\n", line)
	}
	m.setInput("/vim-mode ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.vimMode || len(persisted) != 1 || !persisted[0] || m.status != "Vim mode: on" || !strings.Contains(m.transcript.String(), "Vim mode: on") {
		t.Fatalf("enabled=%v persisted=%v status=%q command=%v", m.vimMode, persisted, m.status, command != nil)
	}
	m.scrollFocused = true
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	m = updated.(*model)
	if m.scroll != 1 {
		t.Fatalf("vim navigation scroll=%d", m.scroll)
	}
	m.scrollFocused = false
	m.setInput("/vim-mode")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.vimMode || len(persisted) != 2 || persisted[1] || m.status != "Vim mode: off" || !strings.Contains(m.transcript.String(), "Vim mode: off") {
		t.Fatalf("disabled=%v persisted=%v status=%q command=%v", !m.vimMode, persisted, m.status, command != nil)
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "`/vim-mode`") {
		t.Fatalf("help missing vim command: %q", m.transcript.String())
	}
}

func TestVimModeCommandRollsBackPersistenceFailure(t *testing.T) {
	for _, initial := range []bool{false, true} {
		m := &model{
			width: 60, height: 16, vimMode: initial,
			persistVimMode: func(bool) error { return errors.New("disk full") },
		}
		m.setInput("/vim-mode")
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.vimMode != initial || m.transcript.Len() != 0 || m.status != "persist vim mode: disk full" {
			t.Fatalf("initial=%v mode=%v transcript=%q status=%q command=%v", initial, m.vimMode, m.transcript.String(), m.status, command != nil)
		}
	}
}

func TestTimestampsCommandPersistsAndRendersMessages(t *testing.T) {
	at := time.Date(2026, 7, 23, 3, 4, 0, 0, time.Local)
	messages := []session.Message{
		{Role: "user", Text: "hello", Time: at},
		{Role: "assistant", Text: "hi", Time: at.Add(time.Minute)},
	}
	var persisted []bool
	m := &model{
		width: 80, height: 24, showTimestamps: true,
		persistTimestamps: func(enabled bool) error {
			persisted = append(persisted, enabled)
			return nil
		},
	}
	m.replaceTranscript(session.FormatTranscript(messages), messages)
	if text := m.transcriptText(); !strings.Contains(text, "You  3:04 AM") || !strings.Contains(text, "Gork  3:05 AM") {
		t.Fatalf("timestamps not rendered: %q", text)
	}
	m.setInput("/timestamps ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.showTimestamps || len(persisted) != 1 || persisted[0] || strings.Contains(m.transcriptText(), "3:04 AM") || m.status != "Timestamps: off" {
		t.Fatalf("disabled=%v persisted=%v status=%q text=%q", !m.showTimestamps, persisted, m.status, m.transcriptText())
	}
	m.setInput("/timestamps")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.showTimestamps || len(persisted) != 2 || !persisted[1] || !strings.Contains(m.transcriptText(), "3:04 AM") || strings.Contains(m.transcriptText(), "Timestamps: off  ") {
		t.Fatalf("enabled=%v persisted=%v text=%q", m.showTimestamps, persisted, m.transcriptText())
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "`/timestamps`") {
		t.Fatalf("help missing timestamps command: %q", m.transcript.String())
	}
}

func TestTimestampsCommandRollsBackPersistenceFailure(t *testing.T) {
	m := &model{showTimestamps: true, persistTimestamps: func(bool) error { return errors.New("disk full") }}
	m.setInput("/timestamps")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.showTimestamps || m.transcript.Len() != 0 || m.status != "persist timestamps: disk full" {
		t.Fatalf("enabled=%v transcript=%q status=%q", m.showTimestamps, m.transcript.String(), m.status)
	}
}

func TestBeginTurnTimestampsOnlyMessages(t *testing.T) {
	m := &model{showTimestamps: true}
	m.appendSystem("system")
	m.beginTurn("hello")
	if len(m.transcriptMessages) != 2 {
		t.Fatalf("messages=%#v", m.transcriptMessages)
	}
	text := m.transcriptText()
	if strings.Contains(text, "system  ") || !strings.Contains(text, "You  ") || !strings.Contains(text, "Gork  ") {
		t.Fatalf("unexpected rendered transcript: %q", text)
	}
}

func TestCompactModeCommandPersistsAndChangesMessageSpacing(t *testing.T) {
	messages := []session.Message{
		{Role: "user", Text: "first\n\nparagraph"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Text: "second"},
		{Role: "assistant", Text: "done"},
	}
	var persisted []bool
	m := &model{
		width: 80, height: 24,
		persistCompactMode: func(enabled bool) error {
			persisted = append(persisted, enabled)
			return nil
		},
	}
	m.replaceTranscript(session.FormatTranscript(messages), messages)
	if text := m.transcriptText(); !strings.Contains(text, "first\n\nparagraph\n\nGork") {
		t.Fatalf("expanded transcript=%q", text)
	}
	m.setInput("/compact-mode ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	text := m.transcriptText()
	if command != nil || !m.compactMode || len(persisted) != 1 || !persisted[0] || m.status != "Compact mode: on" || !strings.Contains(text, "first\n\nparagraph\nGork") || strings.Contains(text, "paragraph\n\nGork") || strings.Contains(text, "answer\n\nYou") {
		t.Fatalf("enabled=%v persisted=%v status=%q text=%q", m.compactMode, persisted, m.status, text)
	}
	m.setInput("/compact-mode")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.compactMode || len(persisted) != 2 || persisted[1] || m.status != "Compact mode: off" || !strings.Contains(m.transcriptText(), "paragraph\n\nGork") {
		t.Fatalf("disabled=%v persisted=%v status=%q text=%q", !m.compactMode, persisted, m.status, m.transcriptText())
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "`/compact-mode`") {
		t.Fatalf("help missing compact-mode command: %q", m.transcript.String())
	}
}

func TestCompactModeKeepsSpacingBetweenAssistantBoundaries(t *testing.T) {
	messages := []session.Message{
		{Role: "assistant", Text: "first"},
		{Role: "assistant", Text: "second"},
	}
	m := &model{height: 24, compactMode: true}
	m.replaceTranscript(session.FormatTranscript(messages), messages)
	if text := m.transcriptText(); !strings.Contains(text, "first\n\nGork\nsecond") {
		t.Fatalf("assistant boundary was compacted: %q", text)
	}
}

func TestCompactModeAutoEnablesForSmallTerminal(t *testing.T) {
	m := &model{height: 20}
	if !m.effectiveCompact() {
		t.Fatal("20-row terminal did not enable auto-compact")
	}
	m.height = 21
	if m.effectiveCompact() {
		t.Fatal("21-row terminal unexpectedly enabled auto-compact")
	}
	m.compactMode = true
	if !m.effectiveCompact() {
		t.Fatal("user compact mode was not honored")
	}
	m.height = 20
	m.persistCompactMode = func(bool) error { return nil }
	m.setInput("/compact-mode")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.compactMode || m.status != "Compact mode: off (auto-compact active on small terminal)" {
		t.Fatalf("mode=%v status=%q", m.compactMode, m.status)
	}
}

func TestCompactModeCommandRollsBackPersistenceFailure(t *testing.T) {
	for _, initial := range []bool{false, true} {
		m := &model{height: 24, compactMode: initial, persistCompactMode: func(bool) error { return errors.New("disk full") }}
		m.setInput("/compact-mode")
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.compactMode != initial || m.transcript.Len() != 0 || m.status != "persist compact mode: disk full" {
			t.Fatalf("initial=%v mode=%v transcript=%q status=%q", initial, m.compactMode, m.transcript.String(), m.status)
		}
	}
}

func TestSideQuestionViewerTimestampFollowsSetting(t *testing.T) {
	at := time.Date(2026, 7, 23, 15, 4, 0, 0, time.Local)
	m := &model{showTimestamps: true, viewer: &readOnlyViewer{title: "Side question", content: "answer", at: at}}
	if content := m.viewerContent(); !strings.Contains(content, "Side question  3:04 PM") {
		t.Fatalf("timestamped viewer=%q", content)
	}
	m.showTimestamps = false
	if content := m.viewerContent(); strings.Contains(content, "3:04 PM") {
		t.Fatalf("hidden timestamp viewer=%q", content)
	}
}

func TestScrollbackFocusAndNavigation(t *testing.T) {
	m := &model{runner: &agent.Runner{}, width: 80, height: 12, status: "ready", history: []string{"old prompt"}, historyIndex: -1, vimMode: true}
	for line := 0; line < 40; line++ {
		fmt.Fprintf(&m.transcript, "line %02d\n", line)
	}
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}

	press(tea.Key{Code: tea.KeyTab})
	if !m.scrollFocused || !strings.Contains(m.View().Content, "SCROLLBACK") {
		t.Fatalf("scrollback focus=%v view=%q", m.scrollFocused, m.View().Content)
	}
	press(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	if !m.scrollFocused || m.status != "plan mode unavailable" {
		t.Fatalf("shift-tab focus=%v status=%q", m.scrollFocused, m.status)
	}
	press(tea.Key{Code: tea.KeyUp})
	press(tea.Key{Code: tea.KeyEnter})
	if !m.scrollFocused || len(m.input) != 0 || m.historyActive {
		t.Fatalf("unbound key escaped scrollback: focus=%v input=%q history=%v", m.scrollFocused, m.input, m.historyActive)
	}
	press(tea.Key{Code: 'k', Mod: tea.ModCtrl})
	if m.scroll != 1 {
		t.Fatalf("ctrl-k scroll=%d", m.scroll)
	}
	press(tea.Key{Code: 'j', Mod: tea.ModCtrl})
	if m.scroll != 0 {
		t.Fatalf("ctrl-j scroll=%d", m.scroll)
	}
	press(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	if m.scroll != max(m.contentHeight()/2, 1) {
		t.Fatalf("ctrl-u scroll=%d", m.scroll)
	}
	press(tea.Key{Code: 'd', Mod: tea.ModCtrl})
	if m.scroll != 0 {
		t.Fatalf("ctrl-d scroll=%d", m.scroll)
	}
	press(tea.Key{Code: tea.KeyPgUp})
	if m.scroll != m.contentHeight() {
		t.Fatalf("page-up scroll=%d", m.scroll)
	}
	press(tea.Key{Code: tea.KeyPgDown})
	if m.scroll != 0 {
		t.Fatalf("page-down scroll=%d", m.scroll)
	}
	press(tea.Key{Code: 'k', Text: "k"})
	if m.scroll != 1 {
		t.Fatalf("vim k scroll=%d", m.scroll)
	}
	press(tea.Key{Code: 'j', Text: "j"})
	if m.scroll != 0 {
		t.Fatalf("vim j scroll=%d", m.scroll)
	}
	m.selection = &textSelection{}
	press(tea.Key{Code: 'g', Text: "g"})
	if m.scroll != m.maxTranscriptScroll() || m.selection != nil {
		t.Fatalf("top scroll=%d max=%d selection=%#v", m.scroll, m.maxTranscriptScroll(), m.selection)
	}
	press(tea.Key{Code: 'k', Mod: tea.ModCtrl})
	if m.scroll != m.maxTranscriptScroll() {
		t.Fatalf("top clamp scroll=%d max=%d", m.scroll, m.maxTranscriptScroll())
	}
	press(tea.Key{Code: 'G', Text: "G"})
	if m.scroll != 0 {
		t.Fatalf("bottom scroll=%d", m.scroll)
	}
	press(tea.Key{Code: tea.KeyEsc})
	if !m.scrollFocused {
		t.Fatal("escape left scrollback")
	}
	press(tea.Key{Code: 'x', Text: "x"})
	if m.scrollFocused || string(m.input) != "x" {
		t.Fatalf("typed focus=%v input=%q", m.scrollFocused, m.input)
	}
	press(tea.Key{Code: tea.KeyPgUp})
	if m.scroll != 0 {
		t.Fatalf("prompt page-up scroll=%d", m.scroll)
	}
	press(tea.Key{Code: tea.KeyTab})
	press(tea.Key{Code: ' ', Text: " "})
	if m.scrollFocused || string(m.input) != "x" {
		t.Fatalf("space focus=%v input=%q", m.scrollFocused, m.input)
	}
	press(tea.Key{Code: tea.KeyTab})
	press(tea.Key{Code: '/', Text: "/"})
	if !m.scrollFocused || m.scrollSearch == nil || string(m.input) != "x" {
		t.Fatalf("slash focus=%v search=%#v input=%q", m.scrollFocused, m.scrollSearch, m.input)
	}
	press(tea.Key{Code: tea.KeyEsc})

	command := m.View().OnMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseLeft}))
	if command == nil {
		t.Fatal("transcript click was ignored")
	}
	updated, _ := m.Update(command())
	m = updated.(*model)
	if !m.scrollFocused {
		t.Fatal("transcript click did not focus scrollback")
	}
}

func TestSimpleScrollbackKeysReturnToPrompt(t *testing.T) {
	for _, key := range []rune{'g', 'G', 'j', 'k', '/'} {
		t.Run(string(key), func(t *testing.T) {
			m := &model{width: 80, height: 12, status: "ready", scrollFocused: true}
			m.transcript.WriteString("line\n")
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: key, Text: string(key)}))
			m = updated.(*model)
			if m.scrollFocused || string(m.input) != string(key) || m.scrollSearch != nil {
				t.Fatalf("key=%q focus=%v input=%q search=%#v", key, m.scrollFocused, m.input, m.scrollSearch)
			}
		})
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

func TestMultilineSlashCommandAndAlias(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, status: "ready"}
	m.setInput("/multiline ignored")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.multiline || m.running || m.status != "multiline input" || m.transcript.Len() != 0 {
		t.Fatalf("enable command=%v multiline=%v running=%v status=%q transcript=%q", command != nil, m.multiline, m.running, m.status, m.transcript.String())
	}
	m.setInput("/ml")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	m = updated.(*model)
	if command != nil || m.multiline || m.running || m.status != "single-line input" || m.transcript.Len() != 0 {
		t.Fatalf("disable command=%v multiline=%v running=%v status=%q transcript=%q", command != nil, m.multiline, m.running, m.status, m.transcript.String())
	}
}

func TestFeedbackCommandUsesLocalSubmissionWithoutModelTurn(t *testing.T) {
	var saved []session.UserFeedback
	runner := &agent.Runner{ModelID: "current-profile", Model: "current-model", SubmitFeedback: func(feedback session.UserFeedback) error {
		saved = append(saved, feedback)
		return nil
	}}
	m := &model{ctx: context.Background(), runner: runner, width: 60, height: 16, status: "ready"}
	m.setInput("/feedback direct feedback")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.status != "saving feedback" || m.transcript.Len() != 0 {
		t.Fatalf("direct start command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.running || m.status != "feedback saved" || len(saved) != 1 || saved[0].Text != "direct feedback" || saved[0].ClientType != "tui" || saved[0].ModelID != "current-profile" || saved[0].ResolvedModelID != "current-model" || saved[0].TurnNumber == nil || *saved[0].TurnNumber != 0 {
		t.Fatalf("direct result running=%v status=%q saved=%#v", m.running, m.status, saved)
	}
	if strings.Contains(m.transcript.String(), "You\n") || !strings.Contains(m.transcript.String(), "Feedback saved locally") {
		t.Fatalf("feedback started a model turn or omitted result: %q", m.transcript.String())
	}

	m.setInput("/feedback")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.feedbackInput || m.status != "feedback mode" || !strings.Contains(stripUIANSI(m.View().Content), "~ ") {
		t.Fatalf("feedback mode command=%v active=%v status=%q view=%q", command != nil, m.feedbackInput, m.status, stripUIANSI(m.View().Content))
	}
	m.setInput("mode feedback")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.feedbackInput || !m.running {
		t.Fatalf("mode submit command=%v active=%v running=%v", command != nil, m.feedbackInput, m.running)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if len(saved) != 2 || saved[1].Text != "mode feedback" {
		t.Fatalf("mode feedback=%#v", saved)
	}

	m.setInput("/feedback")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.feedbackInput || m.status != "feedback required" || !strings.Contains(m.transcript.String(), "Please provide feedback text.") || len(saved) != 2 {
		t.Fatalf("empty command=%v active=%v status=%q transcript=%q saved=%d", command != nil, m.feedbackInput, m.status, m.transcript.String(), len(saved))
	}

	m.setInput("/feedback")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	m.setInput("cancel me")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if command != nil || m.feedbackInput || string(m.input) != "" || m.status != "feedback cancelled" || len(saved) != 2 {
		t.Fatalf("cancel command=%v active=%v input=%q status=%q saved=%d", command != nil, m.feedbackInput, m.input, m.status, len(saved))
	}
}

func TestFeedbackCommandGateHelpAndFailure(t *testing.T) {
	disabled := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 60, height: 16, status: "ready"}
	disabled.setInput("/feedback ignored")
	updated, command := disabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	disabled = updated.(*model)
	if command != nil || disabled.running || disabled.status != "feedback is disabled" || !strings.Contains(disabled.transcript.String(), agent.FeedbackDisabledMessage) {
		t.Fatalf("disabled command=%v running=%v status=%q transcript=%q", command != nil, disabled.running, disabled.status, disabled.transcript.String())
	}
	disabled.setInput("/help")
	updated, _ = disabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	disabled = updated.(*model)
	if strings.Contains(disabled.transcript.String(), "`/feedback [text]`") {
		t.Fatalf("disabled help exposed feedback: %q", disabled.transcript.String())
	}

	enabled := &model{ctx: context.Background(), runner: &agent.Runner{SubmitFeedback: func(session.UserFeedback) error {
		return errors.New("disk full")
	}}, width: 60, height: 16, status: "ready"}
	enabled.setInput("/help")
	updated, _ = enabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	enabled = updated.(*model)
	if !strings.Contains(enabled.transcript.String(), "`/feedback [text]`") {
		t.Fatalf("enabled help omitted feedback: %q", enabled.transcript.String())
	}
	enabled.setInput("/feedback cannot save")
	updated, command = enabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	enabled = updated.(*model)
	updated, _ = enabled.Update(command())
	enabled = updated.(*model)
	if enabled.running || enabled.status != "feedback failed" || !strings.Contains(enabled.transcript.String(), "Feedback could not be saved locally: disk full") {
		t.Fatalf("failure running=%v status=%q transcript=%q", enabled.running, enabled.status, enabled.transcript.String())
	}
}

func TestCopyAssistantMessageUsesSessionTranscript(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "copy-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for index, text := range []string{"first response", "latest response"} {
		if err := logger.AppendPrompt(fmt.Sprintf("prompt-%d", index), nil); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("model_response", map[string]any{"response_id": fmt.Sprintf("r-%d", index), "text": text, "tool_call_count": 0}); err != nil {
			t.Fatal(err)
		}
	}
	m := &model{ctx: context.Background(), runner: &agent.Runner{SessionPath: logger.Path()}, status: "ready"}
	m.setInput("/copy 2")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.status != "copying response" {
		t.Fatalf("copy start command=%v running=%v status=%q", command != nil, m.running, m.status)
	}
	updated, clipboard := m.Update(command())
	m = updated.(*model)
	if clipboard == nil || m.running || m.status != "response copied" {
		t.Fatalf("copy result clipboard=%v running=%v status=%q", clipboard != nil, m.running, m.status)
	}
	if _, err := copyMessageNumber("0"); err == nil {
		t.Fatal("zero copy index was accepted")
	}
	if message := runCopy(&agent.Runner{}, 1)(); message.(copyDoneEvent).err == nil {
		t.Fatal("copy without a session path was accepted")
	}
	empty, err := session.NewLoggerWithID(t.TempDir(), "empty-copy-session")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if err := empty.AppendPrompt("prompt", nil); err != nil {
		t.Fatal(err)
	}
	if err := empty.Append("model_response", map[string]any{"response_id": "empty", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	message := runCopy(&agent.Runner{SessionPath: empty.Path()}, 1)().(copyDoneEvent)
	if message.err != nil || message.text != "" {
		t.Fatalf("empty copy text=%q err=%v", message.text, message.err)
	}
}

func TestInstantInfoCommandsDoNotRunModelTurn(t *testing.T) {
	tests := []struct {
		prompt string
		want   string
		status string
	}{
		{prompt: "/help ignored", want: "# Commands", status: "commands"},
		{prompt: "/session-info ignored", want: "session-1", status: "session info"},
		{prompt: "/status ignored", want: "Turn: 0", status: "session info"},
		{prompt: "/info ignored", want: "Context: 250 / 1000 tokens (25%)", status: "session info"},
		{prompt: "/context ignored", want: "250 / 1000 tokens (25%)", status: "context usage"},
	}
	for _, test := range tests {
		t.Run(test.prompt, func(t *testing.T) {
			m := &model{
				ctx: context.Background(), runner: &agent.Runner{SessionID: "session-1"},
				workspace: "/workspace", modelName: "test-model", inputTokens: 250, contextWindow: 1000,
			}
			m.setInput(test.prompt)
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if command != nil || m.running || m.status != test.status || !strings.Contains(m.transcript.String(), test.want) {
				t.Fatalf("command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
			}
		})
	}

	m := &model{ctx: context.Background(), runner: &agent.Runner{SessionID: "session-1"}}
	m.setInput("/context")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || m.status != "context usage unavailable" || m.transcript.Len() != 0 {
		t.Fatalf("unavailable command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
	}
}

func TestPrivacyCommandsDoNotRunModelTurn(t *testing.T) {
	tests := []struct {
		prompt, want, status string
	}{
		{"/privacy", "Product: Gork Build", "privacy status"},
		{"/privacy opt-out", "Coding data sharing: Opt out", "privacy opt-out"},
		{"/privacy OUT", "Coding data sharing: Opt out", "privacy opt-out"},
		{"/privacy Private", "Coding data sharing: Opt out", "privacy opt-out"},
		{"/privacy opt-in", agent.PrivacyLockedMessage, "privacy locked"},
		{"/privacy IN", agent.PrivacyLockedMessage, "privacy locked"},
		{"/privacy Share", agent.PrivacyLockedMessage, "privacy locked"},
		{"/privacy off", "Unknown argument", "privacy argument invalid"},
	}
	for _, test := range tests {
		t.Run(test.prompt, func(t *testing.T) {
			m := &model{ctx: context.Background(), runner: &agent.Runner{}}
			m.setInput(test.prompt)
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if command != nil || m.running || m.status != test.status || !strings.Contains(m.transcript.String(), test.want) {
				t.Fatalf("command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
			}
		})
	}

	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/help")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(updated.(*model).transcript.String(), "/privacy [opt-out]") {
		t.Fatalf("help=%q", updated.(*model).transcript.String())
	}

	m = &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/privacyx")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running {
		t.Fatalf("/privacyx command=%v running=%v", command != nil, m.running)
	}
}

func TestTerminalSetupCommandsDoNotRunModelTurn(t *testing.T) {
	for _, prompt := range []string{"/terminal-setup", "/terminal-check ignored", "/terminal-info"} {
		t.Run(prompt, func(t *testing.T) {
			m := &model{ctx: context.Background(), runner: &agent.Runner{}}
			m.setInput(prompt)
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if command != nil || m.running || m.status != "terminal setup" || !strings.Contains(m.transcript.String(), "Environment\n") || !strings.Contains(m.transcript.String(), "Clipboard routes") {
				t.Fatalf("command=%v running=%v status=%q transcript=%q", command != nil, m.running, m.status, m.transcript.String())
			}
		})
	}

	m := &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/help")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(updated.(*model).transcript.String(), "/terminal-setup") {
		t.Fatalf("help=%q", updated.(*model).transcript.String())
	}

	m = &model{ctx: context.Background(), runner: &agent.Runner{}}
	m.setInput("/terminal-setupx")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running {
		t.Fatalf("/terminal-setupx command=%v running=%v", command != nil, m.running)
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

func TestPromptSuggestionGhostFollowsInput(t *testing.T) {
	m := &model{suggestionsEnabled: true, promptSuggestion: "run the tests"}
	if got := m.promptSuggestionGhost(); got != "run the tests" {
		t.Fatalf("empty input ghost=%q", got)
	}
	m.setInput("run ")
	if got := m.promptSuggestionGhost(); got != "the tests" {
		t.Fatalf("prefix ghost=%q", got)
	}
	m.setInput("review ")
	if got := m.promptSuggestionGhost(); got != "" {
		t.Fatalf("divergent ghost=%q", got)
	}
	m.setInput("run the tests")
	if got := m.promptSuggestionGhost(); got != "" {
		t.Fatalf("complete ghost=%q", got)
	}
	m.setInput("run ")
	m.cursor--
	if got := m.promptSuggestionGhost(); got != "" {
		t.Fatalf("mid-input ghost=%q", got)
	}
	m.cursor = len(m.input)
	m.suggestionDismissed = true
	if got := m.promptSuggestionGhost(); got != "" {
		t.Fatalf("dismissed ghost=%q", got)
	}
}

func TestPromptSuggestionKeysAcceptDismissAndRestore(t *testing.T) {
	m := &model{suggestionsEnabled: true, promptSuggestion: "run the tests", status: "ready"}
	m.setInput("run ")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if command != nil || string(m.input) != "run the tests" || m.promptSuggestion != "" || m.scrollFocused {
		t.Fatalf("tab command=%v input=%q suggestion=%q focused=%v", command != nil, m.input, m.promptSuggestion, m.scrollFocused)
	}

	m.promptSuggestion = "run the tests"
	m.setInput("run ")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	m = updated.(*model)
	if string(m.input) != "run the tests" {
		t.Fatalf("right input=%q", m.input)
	}

	m.promptSuggestion = "review the diff"
	m.clearInput()
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if !m.suggestionDismissed || m.promptSuggestionGhost() != "" {
		t.Fatalf("dismissed=%v ghost=%q", m.suggestionDismissed, m.promptSuggestionGhost())
	}
	m.suggestionDismissed = false
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if m.promptSuggestionGhost() != "" {
		t.Fatalf("divergent typed ghost=%q", m.promptSuggestionGhost())
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if got := m.promptSuggestionGhost(); got != "review the diff" {
		t.Fatalf("restored ghost=%q", got)
	}
}

func TestPromptSuggestionDropsStaleAndRunningResults(t *testing.T) {
	m := &model{suggestionsEnabled: true, promptSerial: 4}
	updated, _ := m.Update(promptSuggestionEvent{text: "stale", serial: 3})
	m = updated.(*model)
	if m.promptSuggestion != "" {
		t.Fatalf("accepted stale suggestion=%q", m.promptSuggestion)
	}
	m.running = true
	updated, _ = m.Update(promptSuggestionEvent{text: "busy", serial: 4})
	m = updated.(*model)
	if m.promptSuggestion != "" {
		t.Fatalf("accepted running suggestion=%q", m.promptSuggestion)
	}
	m.running = false
	updated, _ = m.Update(promptSuggestionEvent{text: "current", serial: 4})
	m = updated.(*model)
	if m.promptSuggestion != "current" {
		t.Fatalf("current suggestion=%q", m.promptSuggestion)
	}
}

func TestTurnCompletionFetchesPromptSuggestion(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "suggestion")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("implement the feature", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "The feature is implemented.", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	streamer := &promptSuggestionTUIStreamer{}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Client: streamer, SessionPath: logger.Path()},
		workspace: "/work/project", width: 40, height: 16, running: true, suggestionsEnabled: true, promptSerial: 2,
	}
	updated, command := m.Update(turnDoneEvent{result: agent.Result{ResponseID: "r1", Steps: 1}})
	m = updated.(*model)
	if command == nil || m.running {
		t.Fatalf("command=%v running=%v", command != nil, m.running)
	}
	message := command()
	event, ok := message.(promptSuggestionEvent)
	if !ok || event.serial != 2 || event.text != "run the complete test suite" {
		t.Fatalf("event=%#v", message)
	}
	updated, _ = m.Update(event)
	m = updated.(*model)
	if !strings.Contains(m.View().Content, "\x1b[2mrun the complete test suite\x1b[0m") {
		t.Fatalf("suggestion not rendered:\n%s", m.View().Content)
	}
	content, _ := streamer.request.Input[0].Content.(string)
	if streamer.request.Model != "grok-build-0.1" || !strings.Contains(content, "CWD: /work/project") {
		t.Fatalf("request=%#v", streamer.request)
	}
}

func rewindTUIFixture(t *testing.T) (*model, string) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "state.txt")
	if err := os.WriteFile(file, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	t.Cleanup(func() { _ = registry.Close() })
	logger, err := session.NewLoggerWithID(t.TempDir(), "tui-rewind")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	for index, prompt := range []string{"first request", "second request"} {
		if err := logger.AppendPrompt(prompt, nil); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("model_response", map[string]any{"response_id": fmt.Sprintf("response-%d", index+1), "text": fmt.Sprintf("answer %d", index+1), "tool_call_count": 0}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := workspace.NewRewindStore(ws, filepath.Join(t.TempDir(), "rewind.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(1, "state.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(1, "state.txt"); err != nil {
		t.Fatal(err)
	}
	runner := &agent.Runner{Tools: registry, Logger: logger, SessionID: logger.ID(), SessionPath: logger.Path(), Workspace: root}
	if err := runner.EnableRewind(store, 2); err != nil {
		t.Fatal(err)
	}
	m := &model{ctx: context.Background(), runner: runner, workspace: root, previousID: "response-2", width: 60, height: 18, status: "ready", historyIndex: -1}
	m.transcript.WriteString("You\nfirst request\n\nGork\nanswer 1\n\nYou\nsecond request\n\nGork\nanswer 2")
	return m, file
}

func TestTUIRewindPickerPreviewsConflictAndRestoresAll(t *testing.T) {
	m, file := rewindTUIFixture(t)
	m.setInput("/rewind")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.rewind == nil || m.rewind.phase != rewindLoading {
		t.Fatalf("command=%v rewind=%#v", command != nil, m.rewind)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.rewind.phase != rewindPicker || len(m.rewind.points) != 2 || m.rewind.points[0].PromptIndex != 1 || !strings.Contains(stripUIANSI(m.View().Content), "Turn 2") {
		t.Fatalf("rewind=%#v view=%q", m.rewind, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.rewind.phase != rewindModeSelect || len(rewindModes(m.rewind.points, m.rewind.target)) != 3 {
		t.Fatalf("mode state=%#v", m.rewind)
	}
	if err := os.WriteFile(file, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.rewind.phase != rewindPreviewing {
		t.Fatalf("preview command=%v state=%#v", command != nil, m.rewind)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.rewind.phase != rewindConfirm || len(m.rewind.preview.Conflicts) != 1 || !strings.Contains(stripUIANSI(m.View().Content), "External changes will be overwritten") {
		t.Fatalf("confirm state=%#v view=%q", m.rewind, stripUIANSI(m.View().Content))
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	m = updated.(*model)
	if command == nil || m.rewind.phase != rewindExecuting {
		t.Fatalf("execute command=%v state=%#v", command != nil, m.rewind)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.rewind != nil || m.previousID != "response-1" || string(m.input) != "second request" || strings.Contains(m.transcript.String(), "second request") || len(m.transcriptMessages) != 2 {
		t.Fatalf("previous=%q input=%q transcript=%q rewind=%#v", m.previousID, m.input, m.transcript.String(), m.rewind)
	}
	if current, _ := os.ReadFile(file); string(current) != "first" {
		t.Fatalf("restored file=%q", current)
	}
}

func TestTUIRewindCanCancelRunningTurn(t *testing.T) {
	m, _ := rewindTUIFixture(t)
	cancelled := false
	_, cancel := context.WithCancel(context.Background())
	m.turnCancel = func() {
		cancelled = true
		cancel()
	}
	m.running = true
	m.setInput("/rewind")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.rewind == nil || m.rewind.phase != rewindCancelOffer {
		t.Fatalf("command=%v state=%#v", command != nil, m.rewind)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	m = updated.(*model)
	if !cancelled || m.rewind.phase != rewindCancelling {
		t.Fatalf("cancelled=%v state=%#v", cancelled, m.rewind)
	}
	updated, command = m.Update(turnDoneEvent{err: context.Canceled})
	m = updated.(*model)
	if command == nil || m.running || m.rewind.phase != rewindLoading {
		t.Fatalf("command=%v running=%v state=%#v", command != nil, m.running, m.rewind)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.rewind.phase != rewindPicker {
		t.Fatalf("state=%#v", m.rewind)
	}
}

func TestTUIRewindDoubleEscapeAndStaleEvents(t *testing.T) {
	m, _ := rewindTUIFixture(t)
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if command != nil || m.rewind != nil {
		t.Fatalf("first escape command=%v rewind=%#v", command != nil, m.rewind)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if command == nil || m.rewind == nil || m.rewind.phase != rewindLoading {
		t.Fatalf("second escape command=%v rewind=%#v", command != nil, m.rewind)
	}
	serial := m.promptSerial
	updated, _ = m.Update(rewindPointsEvent{points: []session.RewindPoint{{PromptIndex: 99}}, serial: serial - 1})
	m = updated.(*model)
	if m.rewind.phase != rewindLoading {
		t.Fatalf("stale event changed state=%#v", m.rewind)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.rewind.phase != rewindPicker {
		t.Fatalf("current event state=%#v", m.rewind)
	}
}

func TestTUIRewindNavigationBackAndEmptyState(t *testing.T) {
	first, second := "first", "second"
	m := &model{status: "ready", promptSerial: 3, rewind: &rewindState{
		phase: rewindPicker,
		points: []session.RewindPoint{
			{PromptIndex: 1, PromptPreview: &second, HasFileChanges: true},
			{PromptIndex: 0, PromptPreview: &first},
		},
	}}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	if m.rewind.selected != 1 {
		t.Fatalf("selected=%d", m.rewind.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.rewind.phase != rewindModeSelect || m.rewind.target != 1 {
		t.Fatalf("state=%#v", m.rewind)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	if m.rewind.selected != 1 {
		t.Fatalf("mode selected=%d", m.rewind.selected)
	}
	m.rewind.phase = rewindConfirm
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(*model)
	if m.rewind.phase != rewindModeSelect {
		t.Fatalf("confirm back state=%#v", m.rewind)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.rewind.phase != rewindPicker {
		t.Fatalf("mode back state=%#v", m.rewind)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.rewind != nil {
		t.Fatalf("picker was not dismissed=%#v", m.rewind)
	}

	m.rewind = &rewindState{phase: rewindLoading}
	updated, _ = m.Update(rewindPointsEvent{serial: m.promptSerial})
	m = updated.(*model)
	if m.rewind.phase != rewindError || !strings.Contains(m.rewind.err, "No turns") {
		t.Fatalf("empty state=%#v", m.rewind)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if updated.(*model).rewind != nil {
		t.Fatal("error state was not dismissed")
	}
}

func TestSelectedWindowKeepsCurrentRewindPointVisible(t *testing.T) {
	lines := []string{"zero", "one", "two", "three", "four", "five"}
	for selected := range lines {
		window := selectedWindow(lines, selected, 3)
		if !strings.Contains(window, "> "+lines[selected]) || strings.Count(window, "\n") > 2 {
			t.Fatalf("selected=%d window=%q", selected, window)
		}
	}
}

func TestRewindModesAlwaysOfferConversationAndCombinedRewind(t *testing.T) {
	withoutFiles := rewindModes([]session.RewindPoint{{PromptIndex: 0}}, 0)
	if !slices.Equal(withoutFiles, []agent.RewindMode{agent.RewindAll, agent.RewindConversationOnly}) {
		t.Fatalf("modes without files=%#v", withoutFiles)
	}
	withFiles := rewindModes([]session.RewindPoint{{PromptIndex: 0, HasFileChanges: true}}, 0)
	if !slices.Equal(withFiles, []agent.RewindMode{agent.RewindAll, agent.RewindConversationOnly, agent.RewindFilesOnly}) {
		t.Fatalf("modes with files=%#v", withFiles)
	}
}

func modelTUIFixture(t *testing.T) (*model, *modelTUIStreamer) {
	t.Helper()
	logger, err := session.NewLoggerWithID(t.TempDir(), "tui-model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	if err := logger.AppendPrompt("existing prompt", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "old-response", "text": "existing answer", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	streamer := &modelTUIStreamer{}
	runner := &agent.Runner{
		Logger: logger, SessionPath: logger.Path(), ModelID: "plain", Model: "plain-api", ReasoningEffort: "",
		ModelOptions: []agent.ModelOption{
			{ID: "plain", Model: "plain-api", Name: "Plain"},
			{ID: "reasoning", Model: "reasoning-api", Name: "Reasoning X", SupportsReasoningEffort: true, ReasoningEffort: "low", ReasoningEfforts: []agent.ReasoningEffortOption{{ID: "low", Value: "low", Label: "Low"}, {ID: "max", Value: "xhigh", Label: "Maximum"}}},
			{ID: "hidden", Name: "Hidden", Hidden: true},
		},
		ResolveModel: func(id string) (agent.ModelRuntime, error) {
			return agent.ModelRuntime{ID: id, Client: streamer, Model: id + "-api", ContextWindow: 4096, CompactThresholdPercent: 80, ReasoningEffort: "low", SupportsReasoningEffort: id == "reasoning"}, nil
		},
	}
	m := &model{ctx: context.Background(), runner: runner, modelName: "Plain", previousID: "old-response", width: 60, height: 18, status: "ready", suggestionsEnabled: true}
	m.transcript.WriteString("You\nexisting prompt\n\nGork\nexisting answer")
	return m, streamer
}

func TestTUIModelPickerSwitchesModelAndEffort(t *testing.T) {
	m, streamer := modelTUIFixture(t)
	m.setInput("/model")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.modelSelect == nil || m.modelSelect.phase != modelSelectModel || len(m.modelSelect.models) != 2 || strings.Contains(stripUIANSI(m.View().Content), "Hidden") {
		t.Fatalf("command=%v state=%#v view=%q", command != nil, m.modelSelect, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect.phase != modelSelectEffort || len(m.modelSelect.efforts) != 2 {
		t.Fatalf("effort state=%#v", m.modelSelect)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.modelSelect != nil || m.runner.ModelID != "reasoning" || m.runner.ReasoningEffort != "xhigh" || m.modelName != "Reasoning X" || m.previousID != "" || m.contextWindow != 4096 || m.inputTokens != 0 {
		t.Fatalf("command=%v state=%#v runner=%#v model=%q previous=%q", command != nil, m.modelSelect, m.runner, m.modelName, m.previousID)
	}
	if len(streamer.history) != 2 || !strings.Contains(m.status, "Reasoning X (xhigh)") || !strings.Contains(m.transcript.String(), "existing answer") {
		t.Fatalf("history=%#v status=%q transcript=%q", streamer.history, m.status, m.transcript.String())
	}
}

func TestTUIModelCommandsAndPickerNavigation(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.setInput("/model Reasoning X max")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.runner.ModelID != "reasoning" || m.runner.ReasoningEffort != "xhigh" {
		t.Fatalf("direct model id=%q effort=%q status=%q", m.runner.ModelID, m.runner.ReasoningEffort, m.status)
	}
	m.setInput("/effort low")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.runner.ReasoningEffort != "low" || !strings.Contains(m.status, "Reasoning X (low)") {
		t.Fatalf("direct effort=%q status=%q", m.runner.ReasoningEffort, m.status)
	}
	m.setInput("/effort")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect == nil || m.modelSelect.phase != modelSelectEffort || !m.modelSelect.effortOnly || m.modelSelect.selected != 0 {
		t.Fatalf("effort picker=%#v", m.modelSelect)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.modelSelect != nil {
		t.Fatalf("effort picker was not dismissed=%#v", m.modelSelect)
	}
	m.setInput("/model")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect.phase != modelSelectEffort {
		t.Fatalf("picker state=%#v", m.modelSelect)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.modelSelect.phase != modelSelectModel {
		t.Fatalf("picker back state=%#v", m.modelSelect)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.modelSelect != nil || m.status != "ready" {
		t.Fatalf("picker dismissed=%#v status=%q", m.modelSelect, m.status)
	}
	m.setInput("/model missing")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.status, "unknown or unavailable") {
		t.Fatalf("invalid status=%q", m.status)
	}
	m.runner.SetDefaultModel = func(string) error { return fmt.Errorf("disk full") }
	m.previousID = "active-response"
	m.setInput("/model Plain")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.runner.ModelID != "plain" || m.modelName != "Plain" || m.previousID != "" || !strings.Contains(m.status, "persist default model") {
		t.Fatalf("partial persistence model=%q name=%q previous=%q status=%q", m.runner.ModelID, m.modelName, m.previousID, m.status)
	}
	m.btwRunning = true
	m.setInput("/model Reasoning X")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(updated.(*model).status, "background model request") || updated.(*model).runner.ModelID != "plain" {
		t.Fatalf("busy status=%q model=%q", updated.(*model).status, updated.(*model).runner.ModelID)
	}
}

func TestRenderPromptInputWithGhostStaysWithinWidth(t *testing.T) {
	lines := renderPromptInputWithGhost([]rune("run "), 4, "the complete test suite", 12, 1)
	if len(lines) != 1 || displayWidth(stripUIANSI(lines[0])) > 12 || !strings.Contains(lines[0], "\x1b[2m") {
		t.Fatalf("lines=%q width=%d", lines, displayWidth(stripUIANSI(lines[0])))
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

	m = &model{width: 60, height: 16, scroll: 10, scrollLines: 5, invertScroll: true}
	view = m.View()
	updated, _ = m.Update(view.OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: 1, Button: tea.MouseWheelUp}))())
	m = updated.(*model)
	if m.scroll != 5 {
		t.Fatalf("inverted wheel-up scroll=%d", m.scroll)
	}
	updated, _ = m.Update(m.View().OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: 1, Button: tea.MouseWheelDown}))())
	m = updated.(*model)
	if m.scroll != 10 {
		t.Fatalf("inverted wheel-down scroll=%d", m.scroll)
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
	m.scrollFocused = true
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if updated.(*model).selection != nil {
		t.Fatal("keyboard scroll did not clear selection")
	}
}

func TestTextSelectionModesAndSemanticRanges(t *testing.T) {
	for _, test := range []struct {
		value      string
		want       textSelectionMode
		holds      bool
		selectWord bool
	}{
		{value: "flash", want: selectionFlash},
		{value: "hold", want: selectionHold, holds: true},
		{value: "word_select", want: selectionWord, holds: true, selectWord: true},
		{value: "invalid", want: selectionFlash},
	} {
		mode := parseTextSelectionMode(test.value)
		if mode != test.want || mode.holds() != test.holds || mode.selectsWord() != test.selectWord {
			t.Fatalf("mode %q=%v holds=%v word=%v", test.value, mode, mode.holds(), mode.selectsWord())
		}
	}
	if from, to := semanticDisplayRange("hello world", 2, defaultWordSeparators); from != 0 || to != 5 {
		t.Fatalf("semantic word range=%d..%d", from, to)
	}

	for _, test := range []struct {
		value      string
		column     int
		separators string
		from, to   int
	}{
		{value: "hello world", column: 2, separators: defaultWordSeparators, from: 0, to: 5},
		{value: "hello world", column: 5, separators: defaultWordSeparators, from: 5, to: 6},
		{value: "hello-world", column: 5, separators: defaultWordSeparators, from: 5, to: 6},
		{value: "hello-world", column: 5, separators: "", from: 0, to: 11},
		{value: "a_b", column: 1, separators: defaultWordSeparators, from: 0, to: 3},
		{value: "你 好", column: 0, separators: defaultWordSeparators, from: 0, to: 2},
		{value: "", column: 0, separators: defaultWordSeparators, from: 0, to: 0},
	} {
		from, to := wordDisplayRange(test.value, test.column, test.separators)
		if from != test.from || to != test.to {
			t.Fatalf("word range %q col=%d got=%d..%d want=%d..%d", test.value, test.column, from, to, test.from, test.to)
		}
	}

	line := "see https://example.com, then https://en.wikipedia.org/wiki/Rust_(language)"
	if from, to := semanticDisplayRange(line, 8, defaultWordSeparators); selectDisplayColumns(line, from, to-1) != "https://example.com" {
		t.Fatalf("URL range=%d..%d text=%q", from, to, selectDisplayColumns(line, from, to-1))
	}
	if _, _, ok := urlDisplayRange(line, 23); ok {
		t.Fatal("trailing URL punctuation was selectable as part of the URL")
	}
	if from, to, ok := urlDisplayRange(line, displayWidth("see https://example.com, then ")); !ok || selectDisplayColumns(line, from, to-1) != "https://en.wikipedia.org/wiki/Rust_(language)" {
		t.Fatalf("balanced URL range=%d..%d ok=%v", from, to, ok)
	}
	if _, _, ok := urlDisplayRange("see https://.", 4); ok {
		t.Fatal("scheme-only URL was accepted")
	}
	for _, value := range []string{
		"https://example.com/path_(one)",
		"https://example.com/path_[one]",
		"https://example.com/path_{one}",
		"https://example.com/path_<one>",
	} {
		if got := stripTrailingURLPunctuation(value); got != value {
			t.Fatalf("balanced URL=%q want=%q", got, value)
		}
	}
}

func TestWordSelectionDoubleAndTripleClickCopy(t *testing.T) {
	line := "visit https://example.com, now"
	m := &model{selectionMode: selectionWord, wordSeparators: defaultWordSeparators}
	t0 := time.Unix(100, 0)
	click := func(at time.Time) tea.Cmd {
		updated, command := m.Update(mouseSelectionEvent{
			phase: selectionStart, point: selectionPoint{line: 0, column: 8}, lines: []string{line}, at: at,
		})
		m = updated.(*model)
		return command
	}
	release := func() tea.Cmd {
		updated, command := m.Update(mouseSelectionEvent{phase: selectionRelease, point: selectionPoint{line: 0, column: 8}})
		m = updated.(*model)
		return command
	}
	if command := click(t0); command != nil {
		t.Fatal("single click copied text")
	}
	if command := release(); command != nil || m.selection != nil {
		t.Fatal("single-click release retained a selection")
	}
	command := click(t0.Add(100 * time.Millisecond))
	if command == nil || fmt.Sprint(command()) != "https://example.com" || m.selection == nil || !m.selection.semantic {
		t.Fatalf("double click command=%v selection=%#v", command != nil, m.selection)
	}
	if command := release(); command != nil || m.selection == nil {
		t.Fatal("double-click release cleared the held word")
	}
	command = click(t0.Add(200 * time.Millisecond))
	if command == nil || fmt.Sprint(command()) != line || m.selectionClick.count != 0 {
		t.Fatalf("triple click command=%v click=%#v", command != nil, m.selectionClick)
	}
}

func TestHeldSelectionAndDragClickIsolation(t *testing.T) {
	m := &model{selectionMode: selectionWord, wordSeparators: defaultWordSeparators}
	lines := []string{"drag this"}
	t0 := time.Unix(200, 0)
	updated, _ := m.Update(mouseSelectionEvent{phase: selectionStart, point: selectionPoint{}, lines: lines, at: t0})
	m = updated.(*model)
	updated, _ = m.Update(mouseSelectionEvent{phase: selectionMove, point: selectionPoint{column: 3}})
	m = updated.(*model)
	updated, command := m.Update(mouseSelectionEvent{phase: selectionRelease, point: selectionPoint{column: 3}})
	m = updated.(*model)
	if command == nil || fmt.Sprint(command()) != "drag" || m.selection == nil {
		t.Fatalf("held drag command=%v selection=%#v", command != nil, m.selection)
	}

	updated, _ = m.Update(mouseSelectionEvent{phase: selectionStart, point: selectionPoint{}, lines: lines, at: t0.Add(100 * time.Millisecond)})
	m = updated.(*model)
	if m.selectionClick.count != 1 {
		t.Fatalf("drag was counted as a prior click: %#v", m.selectionClick)
	}
}

func TestWordSelectionIgnoresInvalidLine(t *testing.T) {
	for _, line := range []int{-1, 1} {
		m := &model{selectionMode: selectionWord, wordSeparators: defaultWordSeparators}
		updated, command := m.Update(mouseSelectionEvent{
			phase: selectionStart, point: selectionPoint{line: line}, lines: []string{"only"}, at: time.Now(),
		})
		m = updated.(*model)
		if command != nil || m.selection != nil {
			t.Fatalf("line=%d command=%v selection=%#v", line, command != nil, m.selection)
		}
	}
}

func TestCopyTextSelectionFlashAndEmpty(t *testing.T) {
	m := &model{selectionMode: selectionFlash, selection: &textSelection{
		anchor: selectionPoint{}, head: selectionPoint{column: 3}, lines: []string{"copy"}, moved: true, nonce: 1,
	}}
	if command := m.copyTextSelection(); command == nil || m.status != "selection copied" || m.selection == nil {
		t.Fatalf("flash command=%v status=%q selection=%#v", command != nil, m.status, m.selection)
	}
	m.selection = &textSelection{}
	if command := m.copyTextSelection(); command != nil || m.selection != nil {
		t.Fatalf("empty command=%v selection=%#v", command != nil, m.selection)
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
	lines := renderMarkdown("# Heading\n\n- **bold** and `code`\n12) ordered\n> quoted\n[docs](https://example.com)\n```go\n你好abc\n```", 6)
	rendered := strings.Join(lines, "\n")
	for _, expected := range []string{ansiBold, ansiCyan, ansiYellow, ansiUnderline, "• ", "bold", "12)", "│ ", "docs", "你好"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered markdown missing %q:\n%s", expected, rendered)
		}
	}
	plain := stripMarkdownANSI(rendered)
	flat := strings.ReplaceAll(plain, "\n", "")
	for _, expected := range []string{"Heading", "ordered", "code", "https://example.com", "你好abc"} {
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

func TestRenderMarkdownOrderedListMarkers(t *testing.T) {
	raw := strings.Join(renderMarkdown("1. first\n42) second\n1.2 release\n1234567890. plain", 80), "\n")
	if !strings.Contains(raw, ansiYellow+"1.") || !strings.Contains(raw, ansiYellow+"42)") {
		t.Fatalf("ordered markers were not styled: %q", raw)
	}
	rendered := stripMarkdownANSI(raw)
	if !strings.Contains(rendered, "1. first\n42) second") {
		t.Fatalf("ordered markers were not preserved: %q", rendered)
	}
	for _, plain := range []string{"1.2 release", "1234567890. plain"} {
		if !strings.Contains(rendered, plain) {
			t.Fatalf("non-list text changed: %q", rendered)
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
