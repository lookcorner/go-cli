package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestCDRequiresPathAndRestartsWithResolvedWorkspace(t *testing.T) {
	root := t.TempDir()
	called := ""
	m := &model{ctx: context.Background(), runner: &agent.Runner{ChangeWorkspace: func(_ context.Context, path string) (string, error) {
		called = path
		return path, nil
	}}}
	m.setInput("/cd")
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd != nil || m.status != "workspace path required" {
		t.Fatalf("status=%q cmd=%v", m.status, cmd != nil)
	}
	m.setInput("/cd " + root)
	updated, cmd = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if cmd == nil || !m.running {
		t.Fatal("/cd did not start asynchronously")
	}
	updated, quit := m.Update(cmd())
	m = updated.(*model)
	if called != root || m.resumeSession == nil || m.resumeSession.Workspace != root || quit == nil {
		t.Fatalf("called=%q resume=%#v quit=%v", called, m.resumeSession, quit != nil)
	}
}

func TestCDFailureStaysInSession(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{ChangeWorkspace: func(context.Context, string) (string, error) {
		return "", errors.New("not a directory")
	}}}
	m.setInput("/cd missing")
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, quit := m.Update(cmd())
	m = updated.(*model)
	if m.resumeSession != nil || quit != nil || m.status != "workspace change failed: not a directory" {
		t.Fatalf("resume=%#v quit=%v status=%q", m.resumeSession, quit != nil, m.status)
	}
}
