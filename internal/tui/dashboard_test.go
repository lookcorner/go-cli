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
	if live == nil || !m.dashboard.ticking || m.dashboard.loading || len(m.dashboard.rows) != 4 || m.dashboard.rows[1].kind != dashboardStoredSession || m.dashboard.rows[1].title != "Other work" {
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

	m.dashboard.selected = 2
	pressDashboardKey(t, m, tea.Key{Code: 't', Mod: tea.ModCtrl})
	if m.dashboard.err != "Only sessions can be pinned" {
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
	if m.dashboardPins["ghost"] || !slices.Equal(m.dashboardOrder, []string{"b", "a"}) || m.dashboard.rows[1].id != "b" {
		t.Fatalf("pins=%v order=%v rows=%#v", m.dashboardPins, m.dashboardOrder, m.dashboard.rows)
	}
	m.dashboard.selected = 1
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown, Mod: tea.ModShift})
	if !slices.Equal(persisted, []string{"a", "b"}) || m.dashboard.rows[m.dashboard.selected].id != "b" || m.dashboard.selected != 2 || m.status != "session moved down" {
		t.Fatalf("persisted=%v selected=%d rows=%#v status=%q", persisted, m.dashboard.selected, m.dashboard.rows, m.status)
	}
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp, Mod: tea.ModShift})
	if !slices.Equal(persisted, []string{"b", "a"}) || m.dashboard.rows[m.dashboard.selected].id != "b" || m.dashboard.selected != 1 || m.status != "session moved up" {
		t.Fatalf("persisted=%v selected=%d rows=%#v status=%q", persisted, m.dashboard.selected, m.dashboard.rows, m.status)
	}

	m.persistOrder = func([]string) error { return errors.New("read only") }
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyDown, Mod: tea.ModShift})
	if !slices.Equal(m.dashboardOrder, []string{"b", "a"}) || m.dashboard.err != "read only" || m.status != "reorder session failed" {
		t.Fatalf("order=%v state=%#v status=%q", m.dashboardOrder, m.dashboard, m.status)
	}

	m.dashboard.selected = 4
	pressDashboardKey(t, m, tea.Key{Code: tea.KeyUp, Mod: tea.ModShift})
	if m.dashboard.err != "Only sessions can be reordered" {
		t.Fatalf("state=%#v", m.dashboard)
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
