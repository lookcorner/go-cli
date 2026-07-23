package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestHomeCommandRequestsSessionRestart(t *testing.T) {
	m := &model{runner: &agent.Runner{}}
	m.setInput("/home")
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	got := updated.(*model)
	if !got.newSession {
		t.Fatalf("newSession=%v cmd=%v", got.newSession, cmd != nil)
	}
}

func TestAuthCommandsReportUnavailableWithoutRunnerHook(t *testing.T) {
	for _, command := range []string{"/login", "/logout"} {
		m := &model{runner: &agent.Runner{}}
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		got := updated.(*model)
		if cmd != nil || got.running || got.status != "authentication unavailable" {
			t.Fatalf("command=%s status=%q running=%v cmd=%v", command, got.status, got.running, cmd != nil)
		}
	}
}

func TestAuthSuccessRequestsRestartAndFailureStaysOpen(t *testing.T) {
	called := ""
	runner := &agent.Runner{
		Login:  func(context.Context) error { called = "login"; return nil },
		Logout: func(context.Context) error { called = "logout"; return errors.New("offline") },
	}
	m := &model{ctx: context.Background(), runner: runner}
	m.setInput("/login")
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd == nil || !m.running {
		t.Fatal("login did not start asynchronously")
	}
	message := cmd()
	updated, quit := m.Update(message)
	m = updated.(*model)
	if called != "login" || !m.newSession || quit == nil {
		t.Fatalf("login result called=%q newSession=%v quit=%v", called, m.newSession, quit != nil)
	}

	m = &model{ctx: context.Background(), runner: runner}
	m.setInput("/logout")
	updated, cmd = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	message = cmd()
	updated, quit = m.Update(message)
	m = updated.(*model)
	if called != "logout" || m.newSession || quit != nil || m.status != "logout failed: offline" {
		t.Fatalf("logout result called=%q newSession=%v quit=%v status=%q", called, m.newSession, quit != nil, m.status)
	}
}
