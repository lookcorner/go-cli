package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

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
