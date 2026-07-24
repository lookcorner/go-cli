package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestDashboardAliasesOpenTaskOverview(t *testing.T) {
	runner := dashboardFixtureRunner()
	for _, command := range []string{"/dashboard", "/sessions", "/agents-dashboard"} {
		m := &model{runner: runner, workspace: "/work", modelName: "grok"}
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if cmd != nil || m.dashboard == nil || len(m.dashboard.rows) != 3 || !strings.Contains(m.dashboardContent(), "Agent Dashboard") {
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
	m.dashboard.selected = 1
	updated, cmd := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd == nil || !m.dashboard.busy {
		t.Fatalf("view command=%v state=%#v", cmd != nil, m.dashboard)
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)
	if m.dashboard != nil || m.viewer == nil || !strings.Contains(m.viewer.content, "found it") {
		t.Fatalf("viewer=%#v dashboard=%#v", m.viewer, m.dashboard)
	}

	m.viewer = nil
	m.openDashboard()
	m.dashboard.selected = 1
	updated, cmd = m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if cmd == nil || !m.dashboard.busy {
		t.Fatalf("stop command=%v state=%#v", cmd != nil, m.dashboard)
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)
	if running || m.dashboard == nil || m.dashboard.rows[1].status != "completed" || m.status != "subagent stopped" {
		t.Fatalf("running=%v dashboard=%#v status=%q", running, m.dashboard, m.status)
	}
}

func TestDashboardRequiresActiveSession(t *testing.T) {
	m := &model{runner: &agent.Runner{}}
	m.openDashboard()
	if m.dashboard != nil || m.status != "no active session" {
		t.Fatalf("dashboard=%#v status=%q", m.dashboard, m.status)
	}
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
	updated, _ := m.Update(load())
	m = updated.(*model)
	if m.dashboard.loading || len(m.dashboard.rows) != 4 || m.dashboard.rows[1].kind != dashboardStoredSession || m.dashboard.rows[1].title != "Other work" {
		t.Fatalf("rows=%#v", m.dashboard.rows)
	}
	m.dashboard.selected = 1
	updated, quit := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if quit == nil || m.resumeSession == nil || m.resumeSession.Path != other.Path() || m.resumeSession.Workspace != "/other" {
		t.Fatalf("resume=%#v quit=%v", m.resumeSession, quit != nil)
	}

	m = &model{runner: runner, workspace: "/current", modelName: "grok"}
	load = m.openDashboard()
	updated, _ = m.Update(load())
	m = updated.(*model)
	m.dashboard.selected = 1
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

	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyHome})
	pressDashboardKey(t, m, tea.Key{Code: '新', Text: "新"})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyRight})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDelete})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEnd})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyLeft})
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyBackspace})
	pressDashboardKey(t, m, tea.Key{Code: 'o', Text: "o"})
	updated, rename := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if rename == nil || !m.dashboard.busy {
		t.Fatalf("rename=%v state=%#v", rename != nil, m.dashboard)
	}
	updated, _ = m.Update(rename())
	m = updated.(*model)
	info, err := session.InfoByID(dir, "current")
	if err != nil || info.Title != "新Crrent session" || m.dashboard.rows[0].title != info.Title || m.status != "session renamed" {
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
	m.dashboard.selected = 1

	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	m.dashboard.renameInput, m.dashboard.renameCursor = []rune("After"), 5
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyEsc})
	if m.dashboard.renameID != "" || m.dashboard.rows[1].title != "Before" {
		t.Fatalf("rename=%q row=%#v", m.dashboard.renameID, m.dashboard.rows[1])
	}

	pressDashboardKey(t, m, tea.Key{Code: 'e', Text: "e"})
	m.dashboard.renameInput, m.dashboard.renameCursor = []rune("After"), 5
	updated, rename := m.handleDashboardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(rename())
	m = updated.(*model)
	info, err := session.InfoByID(dir, "other")
	if err != nil || info.Title != "After" || m.dashboard.rows[1].title != "After" {
		t.Fatalf("info=%#v err=%v rows=%#v", info, err, m.dashboard.rows)
	}
}

func TestDashboardRejectsInvalidRenameTargetsAndBlankTitles(t *testing.T) {
	m := &model{runner: dashboardFixtureRunner(), workspace: "/work", modelName: "grok"}
	m.openDashboard()
	m.dashboard.selected = 1
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
	if cmd != nil || m.dashboard.err != "Session title must not be blank" || m.dashboard.renameID == "" {
		t.Fatalf("cmd=%v state=%#v", cmd != nil, m.dashboard)
	}
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
