package tui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestDashboardAliasesOpenTaskOverview(t *testing.T) {
	runner := dashboardFixtureRunner()
	for _, command := range []string{"/dashboard", "/sessions", "/agents-dashboard"} {
		m := &model{runner: runner, workspace: "/work", modelName: "grok"}
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if cmd == nil || m.dashboard == nil || len(m.dashboard.rows) != 3 || !strings.Contains(m.dashboardContent(), "Agent Dashboard") {
			t.Fatalf("command=%s dashboard=%#v async=%v", command, m.dashboard, cmd != nil)
		}
	}
}

func TestDashboardViewsAndStopsSubagent(t *testing.T) {
	running := true
	runner := dashboardFixtureRunner()
	runner.ListSubagents = func() []tools.SubagentResult {
		status := "completed"
		if running {
			status = "running"
		}
		return []tools.SubagentResult{{ID: "sub-1", Type: "explore", Description: "inspect", Status: status}}
	}
	runner.GetSubagent = func(context.Context, string, time.Duration) (tools.SubagentResult, error) {
		return tools.SubagentResult{ID: "sub-1", Type: "explore", Description: "inspect", Status: "completed", Output: "found it"}, nil
	}
	runner.KillSubagent = func(context.Context, string) (string, error) { running = false; return "subagent stopped", nil }

	m := &model{runner: runner, workspace: "/work", modelName: "grok"}
	m.openDashboard()
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub-1")
	updated, cmd := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd == nil || !m.dashboard.busy {
		t.Fatalf("view command=%v state=%#v", cmd != nil, m.dashboard)
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)
	if m.dashboard == nil || m.dashboard.peekID != "sub-1" || !strings.Contains(m.dashboard.peekContent, "found it") || m.viewer != nil {
		t.Fatalf("viewer=%#v dashboard=%#v", m.viewer, m.dashboard)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard == nil || m.dashboard.peekID != "" {
		t.Fatalf("dashboard=%#v", m.dashboard)
	}

	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub-1")
	updated, cmd = m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if cmd == nil || !m.dashboard.busy {
		t.Fatalf("stop command=%v state=%#v", cmd != nil, m.dashboard)
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)
	if running || m.dashboard == nil || dashboardRowStatus(m.dashboard.rows, "sub-1") != "completed" || m.status != "subagent stopped" {
		t.Fatalf("running=%v dashboard=%#v status=%q", running, m.dashboard, m.status)
	}
}

func TestDashboardPeeksStoredSessionWithoutSwitching(t *testing.T) {
	m := &model{
		runner:    dashboardFixtureRunner(),
		workspace: "/work",
		modelName: "grok",
		dashboard: &dashboardState{sessions: []session.Info{{
			SessionID: "stored", Title: "Stored", CWD: "/stored", ModelID: "grok-4",
		}}},
	}
	m.refreshDashboard()
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "stored")
	pressDashboardKey(t, m, tea.Key{Code: 'p', Text: "p"})
	if m.resumeSession != nil || m.dashboard.peekID != "stored" || m.dashboard.peekKind != dashboardStoredSession || !strings.Contains(m.dashboard.peekContent, "/stored") {
		t.Fatalf("resume=%#v state=%#v", m.resumeSession, m.dashboard)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnter})
	if !m.dashboard.attached || !strings.HasPrefix(m.dashboardContent(), "# Stored") || strings.Contains(m.dashboardContent(), "Agent Dashboard") {
		t.Fatalf("state=%#v content=%q", m.dashboard, m.dashboardContent())
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard.attached || m.dashboard.peekID != "stored" {
		t.Fatalf("state=%#v", m.dashboard)
	}
	pressDashboardKey(t, m, tea.Key{Code: 'p', Text: "p"})
	if m.dashboard == nil || m.dashboard.peekID != "" {
		t.Fatalf("state=%#v", m.dashboard)
	}
}

func TestDashboardAttachedDetailScrollsAndReturns(t *testing.T) {
	m := &model{
		runner: dashboardFixtureRunner(), width: 40, height: 12,
		dashboard: &dashboardState{
			peekID: "session-1", peekKind: dashboardSession, peekTitle: "Current",
			peekContent: strings.Repeat("detail line\n", 40), attached: true,
		},
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp})
	if m.scroll == 0 || m.scroll > m.maxDashboardScroll() {
		t.Fatalf("scroll=%d max=%d", m.scroll, m.maxDashboardScroll())
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnd})
	if m.scroll != 0 {
		t.Fatalf("scroll=%d", m.scroll)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard.attached || m.dashboard.peekID != "session-1" || m.status != "dashboard details" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}
	m.dashboard.peekID, m.dashboard.peekKind, m.dashboard.attached = "gone", dashboardStoredSession, true
	m.refreshDashboard()
	if m.dashboard.peekID != "" || m.dashboard.attached {
		t.Fatalf("stale attached detail survived refresh: %#v", m.dashboard)
	}
}

func TestDashboardPeeksSynchronousRows(t *testing.T) {
	for _, test := range []struct {
		name string
		row  dashboardRow
		want string
	}{
		{name: "current session", row: dashboardRow{kind: dashboardSession, id: "current", title: "Current", cwd: "/work"}, want: "Session: `current`"},
		{name: "process", row: dashboardRow{kind: dashboardProcess, id: "proc", status: "done", title: "Tests", detail: "shell · exit 0", process: tools.ProcessSnapshot{Command: "go test ./...", Output: "ok"}}, want: "go test ./..."},
		{name: "scheduled", row: dashboardRow{kind: dashboardScheduled, id: "loop", scheduled: tools.ScheduledTaskCreated{HumanSchedule: "every hour", Prompt: "check deploy"}}, want: "check deploy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &model{runner: dashboardFixtureRunner(), workspace: "/work", modelName: "grok", dashboard: &dashboardState{}}
			updated, command := m.openDashboardRow(test.row)
			m = updated.(*model)
			if command != nil || m.dashboard.peekID != test.row.id || m.dashboard.peekKind != test.row.kind || !strings.Contains(m.dashboard.peekContent, test.want) {
				t.Fatalf("command=%v state=%#v", command != nil, m.dashboard)
			}
		})
	}
}

func TestDashboardPeekRejectsUnavailableOrStaleSubagent(t *testing.T) {
	m := &model{runner: &agent.Runner{SessionID: "current"}, dashboard: &dashboardState{}}
	updated, command := m.peekDashboardRow(dashboardRow{kind: dashboardSubagent, id: "gone"})
	m = updated.(*model)
	if command != nil || m.dashboard.err != "Subagent details are unavailable" {
		t.Fatalf("command=%v state=%#v", command != nil, m.dashboard)
	}

	m.dashboard.err = ""
	updated, _ = m.Update(dashboardDoneEvent{action: "peek", id: "gone", text: "late"})
	m = updated.(*model)
	if m.dashboard.peekID != "" || m.dashboard.err != "Subagent no longer exists" || m.status != "dashboard action failed" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}
}

func TestDashboardRequiresActiveSession(t *testing.T) {
	m := &model{runner: &agent.Runner{}}
	m.openDashboard()
	if m.dashboard != nil || m.status != "no active session" {
		t.Fatalf("dashboard=%#v status=%q", m.dashboard, m.status)
	}
}

func TestDashboardCreatesNewAgentWithEditedPrompt(t *testing.T) {
	m := &model{runner: dashboardFixtureRunner(), workspace: "/work", modelName: "grok"}
	m.openDashboard()
	pressDashboardKey(t, m, tea.Key{Code: 'n', Text: "n"})
	if !m.dashboard.dispatching || m.status != "new agent prompt" || !strings.Contains(m.dashboardContent(), "New agent: |") {
		t.Fatalf("state=%#v status=%q content=%q", m.dashboard, m.status, m.dashboardContent())
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnter})
	if m.dashboard.err != "Prompt is required" || m.newSession {
		t.Fatalf("state=%#v new=%v", m.dashboard, m.newSession)
	}
	pressDashboardKey(t, m, tea.Key{Code: '界', Text: "检查部署"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyHome})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyRight})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDelete})
	pressDashboardKey(t, m, tea.Key{Code: '查', Text: "查"})
	updated, quit := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if quit == nil || !m.newSession || m.newSessionPrompt != "检查部署" || m.status != "starting new agent" {
		t.Fatalf("quit=%v new=%v prompt=%q status=%q", quit != nil, m.newSession, m.newSessionPrompt, m.status)
	}
}

func TestDashboardCancelsOrRejectsNewAgentComposer(t *testing.T) {
	m := &model{runner: dashboardFixtureRunner(), workspace: "/work", modelName: "grok"}
	m.openDashboard()
	pressDashboardKey(t, m, tea.Key{Code: 'n', Text: "n"})
	pressDashboardKey(t, m, tea.Key{Code: 'x', Text: "draft"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard == nil || m.dashboard.dispatching || len(m.dashboard.dispatchInput) != 0 || m.status != "agent dashboard" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}

	m.running = true
	pressDashboardKey(t, m, tea.Key{Code: 'n', Text: "n"})
	if m.dashboard.dispatching || m.dashboard.err != "Wait for the current request before creating a new agent" {
		t.Fatalf("state=%#v", m.dashboard)
	}
}

func TestDashboardLiveRefreshRejectsStaleTicks(t *testing.T) {
	completed := false
	runner := dashboardFixtureRunner()
	runner.ListTasks = func() []tools.ProcessSnapshot {
		return []tools.ProcessSnapshot{{TaskID: "proc-1", Description: "tests", Completed: completed}}
	}
	m := &model{runner: runner, workspace: "/work", modelName: "grok"}
	if tick := m.openDashboard(); tick == nil || !m.dashboard.ticking {
		t.Fatalf("tick=%v state=%#v", tick != nil, m.dashboard)
	}
	epoch := m.dashboard.epoch
	m.dashboard.err = "keep this error"
	completed = true
	updated, next := m.Update(dashboardTickEvent{epoch: epoch})
	m = updated.(*model)
	if next == nil || dashboardRowStatus(m.dashboard.rows, "proc-1") != "done" || m.dashboard.err != "keep this error" {
		t.Fatalf("next=%v rows=%#v state=%#v", next != nil, m.dashboard.rows, m.dashboard)
	}

	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	completed = false
	m.openDashboard()
	if m.dashboard.epoch == epoch || dashboardRowStatus(m.dashboard.rows, "proc-1") != "running" {
		t.Fatalf("epoch=%d old=%d rows=%#v", m.dashboard.epoch, epoch, m.dashboard.rows)
	}
	completed = true
	updated, stale := m.Update(dashboardTickEvent{epoch: epoch})
	m = updated.(*model)
	if stale != nil || dashboardRowStatus(m.dashboard.rows, "proc-1") != "running" {
		t.Fatalf("stale=%v rows=%#v", stale != nil, m.dashboard.rows)
	}
}

func TestDashboardPollRejectsOlderSessionSnapshot(t *testing.T) {
	runner := dashboardFixtureRunner()
	m := &model{
		runner:    runner,
		workspace: "/work",
		modelName: "grok",
		dashboard: &dashboardState{
			epoch:      3,
			sessionSeq: 2,
			polling:    true,
			sessions:   []session.Info{{SessionID: "old", Title: "Old"}},
		},
	}
	m.finishDashboardPoll(dashboardPollEvent{
		epoch: 3, seq: 1,
		sessions: []session.Info{{SessionID: "stale", Title: "Stale"}},
	})
	if m.dashboard.polling || m.dashboard.sessions[0].SessionID != "old" {
		t.Fatalf("state=%#v", m.dashboard)
	}
	m.dashboard.polling = true
	m.finishDashboardPoll(dashboardPollEvent{
		epoch: 3, seq: 2,
		sessions: []session.Info{{SessionID: "fresh", Title: "Fresh"}},
	})
	if m.dashboard.polling || m.dashboard.sessions[0].SessionID != "fresh" {
		t.Fatalf("state=%#v", m.dashboard)
	}
}

func dashboardRowStatus(rows []dashboardRow, id string) string {
	for _, row := range rows {
		if row.id == id {
			return row.status
		}
	}
	return ""
}

func dashboardRowIndex(t *testing.T, rows []dashboardRow, kind dashboardRowKind, id string) int {
	t.Helper()
	for index, row := range rows {
		if row.kind == kind && row.id == id {
			return index
		}
	}
	t.Fatalf("dashboard row kind=%d id=%q not found in %#v", kind, id, rows)
	return -1
}

func TestDashboardLoadsSwitchesAndDeletesStoredSessions(t *testing.T) {
	dir := t.TempDir()
	current, err := session.NewLoggerWithID(dir, "current")
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Append("session_metadata", map[string]any{"cwd": "/current", "modelId": "grok"}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := session.NewLoggerWithID(dir, "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Append("session_metadata", map[string]any{"cwd": "/other", "modelId": "grok-fast"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Append("session_title", map[string]any{"title": "Other work"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	runner := dashboardFixtureRunner()
	runner.SessionID, runner.SessionPath = "current", current.Path()
	m := &model{runner: runner, workspace: "/current", modelName: "grok"}
	load := m.openDashboard()
	if load == nil || !m.dashboard.loading {
		t.Fatalf("load=%v state=%#v", load != nil, m.dashboard)
	}
	updated, live := m.Update(load())
	m = updated.(*model)
	otherIndex := dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "other")
	if live == nil || !m.dashboard.ticking || m.dashboard.loading || len(m.dashboard.rows) != 4 || m.dashboard.rows[otherIndex].title != "Other work" {
		t.Fatalf("rows=%#v", m.dashboard.rows)
	}
	m.dashboard.selected = otherIndex
	updated, quit := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if quit == nil || m.resumeSession == nil || m.resumeSession.Path != other.Path() || m.resumeSession.Workspace != "/other" {
		t.Fatalf("resume=%#v quit=%v", m.resumeSession, quit != nil)
	}

	m = &model{runner: runner, workspace: "/current", modelName: "grok"}
	load = m.openDashboard()
	updated, _ = m.Update(load())
	m = updated.(*model)
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "other")
	updated, _ = m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	if m.dashboard.pendingDelete != "other" {
		t.Fatalf("pending=%q", m.dashboard.pendingDelete)
	}
	updated, remove := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	m = updated.(*model)
	if remove == nil {
		t.Fatal("delete did not return a command")
	}
	updated, _ = m.Update(remove())
	m = updated.(*model)
	if _, err := os.Stat(other.Path()); !os.IsNotExist(err) || len(m.dashboard.sessions) != 1 || m.status != "session deleted" {
		t.Fatalf("stat=%v sessions=%#v status=%q", err, m.dashboard.sessions, m.status)
	}
}

func TestDashboardRenamesActiveSessionWithUnicodeEditing(t *testing.T) {
	dir := t.TempDir()
	logger, err := session.NewLoggerWithID(dir, "current")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	runner := dashboardFixtureRunner()
	runner.SessionID, runner.SessionPath, runner.Logger = "current", logger.Path(), logger
	m := &model{runner: runner, workspace: "/work", modelName: "grok"}
	load := m.openDashboard()
	updated, _ := m.Update(load())
	m = updated.(*model)

	pressDashboardKey(t, m, tea.Key{Code: 'r', Mod: tea.ModCtrl})
	pressDashboardKey(t, m, tea.Key{Code: 'c', Text: "Current"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyHome})
	pressDashboardKey(t, m, tea.Key{Code: '新', Text: "新"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyRight})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDelete})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnd})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyLeft})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyBackspace})
	pressDashboardKey(t, m, tea.Key{Code: 'n', Text: "n"})
	updated, rename := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if rename == nil || !m.dashboard.busy {
		t.Fatalf("rename=%v state=%#v", rename != nil, m.dashboard)
	}
	updated, _ = m.Update(rename())
	m = updated.(*model)
	info, err := session.InfoByID(dir, "current")
	if err != nil || info.Title != "新Crrent" || m.dashboard.rows[0].title != info.Title || m.status != "session renamed" {
		t.Fatalf("info=%#v err=%v row=%#v status=%q", info, err, m.dashboard.rows[0], m.status)
	}
}

func TestDashboardRenamesStoredSessionAndCancelsEditing(t *testing.T) {
	dir := t.TempDir()
	current, err := session.NewLoggerWithID(dir, "current")
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Append("session_metadata", map[string]any{"cwd": "/work", "modelId": "grok"}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := session.NewLoggerWithID(dir, "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Append("session_metadata", map[string]any{"cwd": "/other", "modelId": "grok"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Append("session_title", map[string]any{"title": "Before"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	runner := dashboardFixtureRunner()
	runner.SessionID, runner.SessionPath = "current", current.Path()
	m := &model{runner: runner, workspace: "/work", modelName: "grok"}
	load := m.openDashboard()
	updated, _ := m.Update(load())
	m = updated.(*model)
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "other")

	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	m.dashboard.renameInput, m.dashboard.renameCursor = []rune("After"), 5
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	otherIndex := dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "other")
	if m.dashboard.renameID != "" || m.dashboard.rows[otherIndex].title != "Before" {
		t.Fatalf("rename=%q row=%#v", m.dashboard.renameID, m.dashboard.rows[otherIndex])
	}

	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	m.dashboard.renameInput, m.dashboard.renameCursor = []rune("After"), 5
	updated, rename := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(rename())
	m = updated.(*model)
	info, err := session.InfoByID(dir, "other")
	otherIndex = dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "other")
	if err != nil || info.Title != "After" || m.dashboard.rows[otherIndex].title != "After" {
		t.Fatalf("info=%#v err=%v rows=%#v", info, err, m.dashboard.rows)
	}
}

func TestDashboardRejectsInvalidRenameTargetsAndBlankTitles(t *testing.T) {
	m := &model{runner: dashboardFixtureRunner(), workspace: "/work", modelName: "grok"}
	m.openDashboard()
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub-1")
	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	if m.dashboard.err != "Only sessions can be renamed" || m.dashboard.renameID != "" {
		t.Fatalf("state=%#v", m.dashboard)
	}
	m.dashboard.selected = 0
	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	m.dashboard.renameInput, m.dashboard.renameCursor = nil, 0
	pressDashboardKey(t, m, tea.Key{Code: 'x', Text: strings.Repeat("x", 101) + "\n"})
	if len(m.dashboard.renameInput) != 100 || strings.ContainsRune(string(m.dashboard.renameInput), '\n') {
		t.Fatalf("rename input length=%d input=%q", len(m.dashboard.renameInput), m.dashboard.renameInput)
	}
	m.dashboard.renameInput, m.dashboard.renameCursor = []rune("   "), 3
	updated, cmd := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd != nil || m.dashboard.err != "" || m.dashboard.renameID != "" || m.status != "agent dashboard" {
		t.Fatalf("cmd=%v state=%#v", cmd != nil, m.dashboard)
	}
}

func TestDashboardPinsSessionsAndPreservesSelection(t *testing.T) {
	var persisted []string
	m := &model{
		runner:        dashboardFixtureRunner(),
		workspace:     "/work",
		modelName:     "grok",
		dashboardPins: map[string]bool{"other": true},
		persistPins: func(ids []string) error {
			persisted = append([]string(nil), ids...)
			return nil
		},
		dashboard: &dashboardState{sessions: []session.Info{{SessionID: "other", Title: "Other", CWD: "/other"}}},
	}
	m.refreshDashboard()
	if m.dashboard.rows[0].id != "other" || !m.dashboard.rows[0].pinned || !strings.Contains(m.dashboardContent(), "* idle") {
		t.Fatalf("rows=%#v\n%s", m.dashboard.rows, m.dashboardContent())
	}
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if len(persisted) != 0 || m.dashboardPins["other"] || m.dashboard.rows[m.dashboard.selected].id != "other" || m.status != "session unpinned" {
		t.Fatalf("persisted=%v pins=%v selected=%d rows=%#v status=%q", persisted, m.dashboardPins, m.dashboard.selected, m.dashboard.rows, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if !m.dashboardPins["other"] || len(persisted) != 1 || persisted[0] != "other" || m.status != "session pinned" {
		t.Fatalf("persisted=%v pins=%v status=%q", persisted, m.dashboardPins, m.status)
	}

	m.persistPins = func([]string) error { return errors.New("disk full") }
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if !m.dashboardPins["other"] || m.dashboard.err != "disk full" || m.status != "pin session failed" {
		t.Fatalf("pins=%v state=%#v status=%q", m.dashboardPins, m.dashboard, m.status)
	}

	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardProcess, "proc-1")
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if m.dashboard.err != "Only sessions and subagents can be pinned" {
		t.Fatalf("state=%#v", m.dashboard)
	}
}

func TestDashboardReordersSessionsAndCleansStaleReferences(t *testing.T) {
	var persisted []string
	m := &model{
		runner:         dashboardFixtureRunner(),
		workspace:      "/work",
		modelName:      "grok",
		dashboardPins:  map[string]bool{"ghost": true},
		dashboardOrder: []string{"ghost", "b", "b", "a"},
		persistOrder: func(ids []string) error {
			persisted = append([]string(nil), ids...)
			return nil
		},
		dashboard: &dashboardState{sessions: []session.Info{
			{SessionID: "a", Title: "A", CWD: "/a"},
			{SessionID: "b", Title: "B", CWD: "/b"},
			{SessionID: "c", Title: "C", CWD: "/c"},
		}},
	}
	m.refreshDashboard()
	if m.dashboardPins["ghost"] || !slices.Equal(m.dashboardOrder, []string{"b", "a"}) || !slices.Equal(dashboardSessionRowIDs(m.dashboard.rows), []string{"session-1", "b", "a", "c"}) {
		t.Fatalf("pins=%v order=%v rows=%#v", m.dashboardPins, m.dashboardOrder, m.dashboard.rows)
	}
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardStoredSession, "b")
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown, Mod: tea.ModShift})
	if !slices.Equal(persisted, []string{"a", "b", "c"}) || m.dashboard.rows[m.dashboard.selected].id != "b" || !slices.Equal(dashboardSessionRowIDs(m.dashboard.rows), []string{"session-1", "a", "b", "c"}) || m.status != "session moved down" {
		t.Fatalf("persisted=%v selected=%d rows=%#v status=%q", persisted, m.dashboard.selected, m.dashboard.rows, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp, Mod: tea.ModShift})
	if !slices.Equal(persisted, []string{"b", "a", "c"}) || m.dashboard.rows[m.dashboard.selected].id != "b" || !slices.Equal(dashboardSessionRowIDs(m.dashboard.rows), []string{"session-1", "b", "a", "c"}) || m.status != "session moved up" {
		t.Fatalf("persisted=%v selected=%d rows=%#v status=%q", persisted, m.dashboard.selected, m.dashboard.rows, m.status)
	}

	m.persistOrder = func([]string) error { return errors.New("read only") }
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown, Mod: tea.ModShift})
	if !slices.Equal(m.dashboardOrder, []string{"b", "a", "c"}) || m.dashboard.err != "read only" || m.status != "reorder session failed" {
		t.Fatalf("order=%v state=%#v status=%q", m.dashboardOrder, m.dashboard, m.status)
	}

	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardProcess, "proc-1")
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp, Mod: tea.ModShift})
	if m.dashboard.err != "Only sessions and subagents can be reordered" {
		t.Fatalf("state=%#v", m.dashboard)
	}
}

func TestDashboardPinsAndReordersSubagentsByPersistentIdentity(t *testing.T) {
	var pinned, ordered []string
	ref := dashboardSubagentRef("session-1", "sub-1")
	m := &model{
		runner:        dashboardFixtureRunner(),
		workspace:     "/work",
		modelName:     "grok",
		dashboardPins: map[string]bool{"sub:session-1:ghost": true},
		dashboardOrder: []string{
			"sub:session-1:ghost",
		},
		persistPins: func(ids []string) error {
			pinned = slices.Clone(ids)
			return nil
		},
		persistOrder: func(ids []string) error {
			ordered = slices.Clone(ids)
			return nil
		},
		dashboard: &dashboardState{},
	}
	m.refreshDashboard()
	if m.dashboardPins["sub:session-1:ghost"] || len(m.dashboardOrder) != 0 {
		t.Fatalf("pins=%v order=%v", m.dashboardPins, m.dashboardOrder)
	}
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub-1")
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if !slices.Equal(pinned, []string{ref}) || !m.dashboardPins[ref] || m.dashboard.rows[0].id != "sub-1" || m.dashboard.rows[m.dashboard.selected].id != "sub-1" || m.status != "subagent pinned" {
		t.Fatalf("pinned=%v pins=%v selected=%d rows=%#v status=%q", pinned, m.dashboardPins, m.dashboard.selected, m.dashboard.rows, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if len(pinned) != 0 || m.dashboardPins[ref] || m.status != "subagent unpinned" {
		t.Fatalf("pinned=%v pins=%v status=%q", pinned, m.dashboardPins, m.status)
	}

	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp, Mod: tea.ModShift})
	if !slices.Equal(ordered, []string{ref, "session-1"}) || m.dashboard.rows[0].id != "sub-1" || m.dashboard.rows[m.dashboard.selected].id != "sub-1" || m.status != "subagent moved up" {
		t.Fatalf("ordered=%v selected=%d rows=%#v status=%q", ordered, m.dashboard.selected, m.dashboard.rows, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown, Mod: tea.ModShift})
	if !slices.Equal(ordered, []string{"session-1", ref}) || m.dashboard.rows[0].id != "session-1" || m.dashboard.rows[m.dashboard.selected].id != "sub-1" || m.status != "subagent moved down" {
		t.Fatalf("ordered=%v selected=%d rows=%#v status=%q", ordered, m.dashboard.selected, m.dashboard.rows, m.status)
	}
}

func TestDashboardPinnedRowsShareAReorderGroup(t *testing.T) {
	if got := dashboardReorderGroup(dashboardRow{pinned: true, status: "idle"}, "state"); got != "pinned" {
		t.Fatalf("group=%q", got)
	}
}

func TestDashboardGroupingPersistsAndPreservesSelection(t *testing.T) {
	var persisted string
	runner := &agent.Runner{
		SessionID:     "current",
		ListSubagents: func() []tools.SubagentResult { return nil },
		ListTasks:     func() []tools.ProcessSnapshot { return nil },
	}
	m := &model{
		runner:            runner,
		workspace:         "/middle",
		modelName:         "grok",
		dashboardGrouping: "state",
		dashboardOrder:    []string{"z", "a"},
		persistGrouping: func(grouping string) error {
			persisted = grouping
			return nil
		},
		dashboard: &dashboardState{sessions: []session.Info{
			{SessionID: "z", Title: "Z", CWD: "/z"},
			{SessionID: "a", Title: "A", CWD: "/a"},
		}},
	}
	m.refreshDashboard()
	if ids := dashboardSessionRowIDs(m.dashboard.rows); !slices.Equal(ids, []string{"current", "z", "a"}) {
		t.Fatalf("state rows=%v", ids)
	}
	content := m.dashboardContent()
	if !strings.Contains(content, "Working (1)") || !strings.Contains(content, "Idle (2)") {
		t.Fatalf("missing state sections:\n%s", content)
	}
	m.dashboard.selected = 2
	pressDashboardKey(t, m, tea.Key{Code: 'g', Mod: tea.ModCtrl})
	if persisted != "directory" || m.dashboardGrouping != "directory" || m.dashboard.rows[m.dashboard.selected].id != "a" {
		t.Fatalf("persisted=%q grouping=%q selected=%d rows=%#v", persisted, m.dashboardGrouping, m.dashboard.selected, m.dashboard.rows)
	}
	if ids := dashboardSessionRowIDs(m.dashboard.rows); !slices.Equal(ids, []string{"a", "current", "z"}) {
		t.Fatalf("directory rows=%v", ids)
	}
	if strings.Contains(m.dashboardContent(), "Working (") || m.status != "dashboard grouped by directory" {
		t.Fatalf("content=%q status=%q", m.dashboardContent(), m.status)
	}

	m.persistGrouping = func(string) error { return errors.New("read only") }
	pressDashboardKey(t, m, tea.Key{Code: 'g', Mod: tea.ModCtrl})
	if m.dashboardGrouping != "directory" || m.dashboard.err != "read only" || m.status != "dashboard grouping failed" {
		t.Fatalf("grouping=%q state=%#v status=%q", m.dashboardGrouping, m.dashboard, m.status)
	}
}

func TestDashboardStateGroupingKeepsPinnedSectionFirst(t *testing.T) {
	m := &model{
		runner:        &agent.Runner{SessionID: "current"},
		workspace:     "/work",
		modelName:     "grok",
		dashboardPins: map[string]bool{"idle": true},
		dashboard:     &dashboardState{sessions: []session.Info{{SessionID: "idle", Title: "Idle", CWD: "/idle"}}},
	}
	m.refreshDashboard()
	content := m.dashboardContent()
	if m.dashboard.rows[0].id != "idle" || strings.Index(content, "Pinned (1)") > strings.Index(content, "Working (1)") {
		t.Fatalf("rows=%#v\n%s", m.dashboard.rows, content)
	}
}

func TestDashboardSearchFiltersLiveAndKeepsConfirmedFilter(t *testing.T) {
	done := true
	runner := &agent.Runner{
		SessionID: "current",
		ListTasks: func() []tools.ProcessSnapshot {
			return []tools.ProcessSnapshot{{TaskID: "tests", Description: "Release Tests", Completed: done, CWD: "/release"}}
		},
	}
	m := &model{
		runner:    runner,
		workspace: "/work",
		modelName: "grok",
		dashboard: &dashboardState{sessions: []session.Info{
			{SessionID: "auth", Title: "Auth Flow", CWD: "/services/auth"},
			{SessionID: "docs", Title: "Write Docs", CWD: "/docs"},
		}},
	}
	m.refreshDashboard()
	pressDashboardKey(t, m, tea.Key{Code: '/', Mod: tea.ModCtrl})
	if !m.dashboard.searching || m.status != "search dashboard" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: 'a', Text: "auth"})
	if len(m.dashboard.rows) != 1 || m.dashboard.rows[0].id != "auth" || !strings.Contains(m.dashboardContent(), "Search: auth|") {
		t.Fatalf("rows=%#v\n%s", m.dashboard.rows, m.dashboardContent())
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnter})
	if m.dashboard.searching || m.dashboard.filterKind != "text" || m.dashboard.filterValue != "auth" || m.status != "dashboard filter applied" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}
	if !strings.Contains(m.dashboardContent(), "Filter: auth") {
		t.Fatalf("content=%q", m.dashboardContent())
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard == nil || m.dashboard.filterKind != "" || len(m.dashboard.rows) != 4 || m.status != "dashboard filter cleared" {
		t.Fatalf("state=%#v status=%q", m.dashboard, m.status)
	}
}

func TestDashboardSearchSupportsAgentAndStateFilters(t *testing.T) {
	runner := &agent.Runner{
		SessionID: "current",
		ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{{ID: "sub", Description: "Implementer", Status: "completed"}}
		},
	}
	m := &model{
		runner:    runner,
		workspace: "/work",
		modelName: "grok",
		dashboard: &dashboardState{sessions: []session.Info{{SessionID: "review", Title: "Reviewer", CWD: "/review"}}},
	}
	m.refreshDashboard()
	pressDashboardKey(t, m, tea.Key{Code: '/', Mod: tea.ModCtrl})
	pressDashboardKey(t, m, tea.Key{Code: 'a', Text: "a:implement"})
	if len(m.dashboard.rows) != 1 || m.dashboard.rows[0].id != "sub" {
		t.Fatalf("agent filter rows=%#v", m.dashboard.rows)
	}
	pressDashboardKey(t, m, tea.Key{Code: '/', Mod: tea.ModCtrl})
	if m.dashboard.searching || m.dashboard.filterKind != "" || len(m.dashboard.rows) != 3 {
		t.Fatalf("cancel state=%#v", m.dashboard)
	}
	pressDashboardKey(t, m, tea.Key{Code: '/', Mod: tea.ModCtrl})
	pressDashboardKey(t, m, tea.Key{Code: 's', Text: "s:done"})
	if len(m.dashboard.rows) != 1 || m.dashboard.rows[0].id != "sub" || m.dashboard.filterKind != "state" || m.dashboard.filterValue != "done" {
		t.Fatalf("state filter rows=%#v state=%#v", m.dashboard.rows, m.dashboard)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard.searching || m.dashboard.filterKind != "" || len(m.dashboard.rows) != 3 {
		t.Fatalf("escape state=%#v", m.dashboard)
	}
}

func TestDashboardSearchEditsUnicodeAndTreatsJKAsText(t *testing.T) {
	m := &model{
		runner:    &agent.Runner{SessionID: "current"},
		workspace: "/工作/jk",
		modelName: "grok",
		dashboard: &dashboardState{},
	}
	m.refreshDashboard()
	pressDashboardKey(t, m, tea.Key{Code: '/', Mod: tea.ModCtrl})
	pressDashboardKey(t, m, tea.Key{Code: 'j', Text: "jk"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyHome})
	pressDashboardKey(t, m, tea.Key{Code: '工', Text: "工"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyRight})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDelete})
	if string(m.dashboard.searchInput) != "工j" || m.dashboard.searchCursor != 2 || len(m.dashboard.rows) != 0 {
		t.Fatalf("input=%q cursor=%d rows=%#v", m.dashboard.searchInput, m.dashboard.searchCursor, m.dashboard.rows)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp})
	if m.dashboard.selected != 0 {
		t.Fatalf("empty filter selection=%d", m.dashboard.selected)
	}
}

func TestDashboardFilterParsing(t *testing.T) {
	tests := []struct {
		query string
		kind  string
		value string
	}{
		{"", "", ""},
		{"a: Reviewer ", "agent", "Reviewer"},
		{"a:", "", ""},
		{"s:needs_input", "state", "awaiting"},
		{"s:busy", "state", "working"},
		{"s:completed", "state", "done"},
		{"s:paused", "state", "blocked"},
		{"s:unknown", "text", "unknown"},
		{"#42", "text", "#42"},
	}
	for _, test := range tests {
		kind, value := dashboardFilter(test.query)
		if kind != test.kind || value != test.value {
			t.Fatalf("query=%q got=(%q,%q) want=(%q,%q)", test.query, kind, value, test.kind, test.value)
		}
	}
}

func TestDashboardShowsLiveSubagentMetrics(t *testing.T) {
	item := tools.SubagentResult{
		ID: "sub", Type: "explore", Description: "Inspect",
		Status: "running", DurationMS: 1500, ToolCalls: 2, TokensUsed: 300,
		Turns: 1, ContextUsage: 25,
	}
	runner := &agent.Runner{
		SessionID:     "current",
		ListSubagents: func() []tools.SubagentResult { return []tools.SubagentResult{item} },
	}
	m := &model{runner: runner, workspace: "/work", modelName: "grok", dashboard: &dashboardState{epoch: 1}}
	m.refreshDashboard()
	index := dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub")
	if detail := m.dashboard.rows[index].detail; detail != "explore · 2s · 2 tools · 300 tok · 1 turn · 25% ctx" {
		t.Fatalf("detail=%q", detail)
	}
	item.DurationMS, item.ToolCalls, item.TokensUsed, item.Turns, item.ContextUsage = 6100, 4, 900, 2, 50
	updated, next := m.Update(dashboardTickEvent{epoch: 1})
	m = updated.(*model)
	index = dashboardRowIndex(t, m.dashboard.rows, dashboardSubagent, "sub")
	if next == nil || m.dashboard.rows[index].detail != "explore · 6s · 4 tools · 900 tok · 2 turns · 50% ctx" {
		t.Fatalf("next=%v detail=%q", next != nil, m.dashboard.rows[index].detail)
	}
}

func TestDashboardProcessMetrics(t *testing.T) {
	exitCode := 7
	start := time.Unix(100, 0)
	end := start.Add(3 * time.Second)
	item := tools.ProcessSnapshot{
		Kind: "shell", Completed: true, ExitCode: &exitCode,
		StartTime: tools.ProcessTime{SecsSinceEpoch: start.Unix()},
		EndTime:   &tools.ProcessTime{SecsSinceEpoch: end.Unix()},
	}
	if detail := dashboardProcessMetrics(item); detail != "shell · exit 7 · 3s" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestDashboardCancelsScheduledTask(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if _, err := registry.Execute(context.Background(), "scheduler_create", []byte(`{"interval":"1h","prompt":"check deployment","recurring":true}`)); err != nil {
		t.Fatal(err)
	}
	scheduled := registry.ScheduledTasks()
	if len(scheduled) != 1 {
		t.Fatalf("scheduled=%#v", scheduled)
	}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{SessionID: "current", Tools: registry},
		workspace: "/work", modelName: "grok",
	}
	m.openDashboard()
	m.dashboard.selected = dashboardRowIndex(t, m.dashboard.rows, dashboardScheduled, scheduled[0].TaskID)
	updated, cancel := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if cancel == nil || !m.dashboard.busy {
		t.Fatalf("cancel=%v state=%#v", cancel != nil, m.dashboard)
	}
	updated, _ = m.Update(cancel())
	m = updated.(*model)
	if len(registry.ScheduledTasks()) != 0 || dashboardRowStatus(m.dashboard.rows, scheduled[0].TaskID) != "" || m.status != "scheduled task cancelled" {
		t.Fatalf("scheduled=%#v rows=%#v status=%q", registry.ScheduledTasks(), m.dashboard.rows, m.status)
	}
}

func TestDashboardReportsUnavailableScheduledCancellation(t *testing.T) {
	m := &model{runner: &agent.Runner{SessionID: "current"}, dashboard: &dashboardState{}}
	updated, command := m.stopDashboardRow(dashboardRow{kind: dashboardScheduled, id: "loop-1"})
	if updated.(*model) != m || command != nil || m.dashboard.err != "Scheduled task cancellation is unavailable" {
		t.Fatalf("command=%v state=%#v", command != nil, m.dashboard)
	}
}

func dashboardSessionRowIDs(rows []dashboardRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.kind == dashboardSession || row.kind == dashboardStoredSession {
			ids = append(ids, row.id)
		}
	}
	return ids
}

func pressDashboardKey(t *testing.T, m *model, key tea.Key) {
	t.Helper()
	updated, cmd := m.handleDashboardKey(tea.KeyPressMsg(key))
	if updated.(*model) != m || cmd != nil {
		t.Fatalf("key=%#v returned unexpected command", key)
	}
}

func dashboardFixtureRunner() *agent.Runner {
	return &agent.Runner{
		SessionID: "session-1",
		ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{{ID: "sub-1", Type: "explore", Status: "running", Description: "inspect"}}
		},
		ListTasks: func() []tools.ProcessSnapshot {
			return []tools.ProcessSnapshot{{TaskID: "proc-1", Command: "go test ./...", Description: "tests"}}
		},
	}
}
