package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestDebugCommandTogglesHUDsAndStaysOutOfHelp(t *testing.T) {
	m := &model{width: 70, height: 16, status: "ready"}
	for _, command := range []string{"/debug scroll", "/debug fps"} {
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if cmd != nil {
			t.Fatalf("%s returned an asynchronous command", command)
		}
	}
	if !m.debug.scrollHUD || !m.debug.fpsHUD {
		t.Fatalf("debug toggles=%#v", m.debug)
	}
	view := m.View().Content
	if !strings.Contains(view, "scroll debug  (/debug scroll)") || !strings.Contains(view, "fps debug  (/debug fps)") {
		t.Fatalf("debug HUDs not rendered: %q", view)
	}

	m.setInput("/debug")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "debug toggles: scroll on, fps on, log off") {
		t.Fatalf("debug status missing: %q", m.transcript.String())
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if strings.Contains(m.transcript.String(), "`/debug") || strings.Contains(m.transcript.String(), "`/scroll-debug") {
		t.Fatalf("hidden debug command appeared in help: %q", m.transcript.String())
	}
}

func TestDebugAliasAndInvalidOption(t *testing.T) {
	m := &model{width: 60, height: 16}
	m.setInput("/scroll-debug")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if !m.debug.scrollHUD || m.status != "scroll debug: on" {
		t.Fatalf("alias state=%#v status=%q", m.debug, m.status)
	}

	m.setInput("/debug wat")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.status != "debug option invalid" || !strings.Contains(m.transcript.String(), `Unknown /debug option "wat"`) || !strings.Contains(m.transcript.String(), "scroll|fps|log") {
		t.Fatalf("invalid option status=%q transcript=%q", m.status, m.transcript.String())
	}
}

func TestDebugFPSUsesBoundedFrameWindow(t *testing.T) {
	var state debugState
	for index := 0; index < debugFrameWindow+20; index++ {
		state.recordFrame(10 * time.Millisecond)
	}
	if len(state.frames) != debugFrameWindow {
		t.Fatalf("frames=%d want=%d", len(state.frames), debugFrameWindow)
	}
	if got := state.frameStats(); got != "fps:100 p50:10.0ms p95:10.0ms" {
		t.Fatalf("frame stats=%q", got)
	}
}

func TestDebugScrollLogRecordsKeyboardAndWheelEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	m := &model{width: 60, height: 16, status: "ready"}
	for line := 0; line < 40; line++ {
		fmt.Fprintf(&m.transcript, "line %02d\n", line)
	}
	m.handleDebugCommand("/debug", "log")
	path := m.debug.logPath
	if path == "" || m.status != "scroll log: on" {
		t.Fatalf("path=%q status=%q", path, m.status)
	}
	m.scrollTranscript(2)
	updated, _ := m.Update(mouseScrollEvent{lines: -1})
	m = updated.(*model)
	m.handleDebugCommand("/debug", "log")
	if m.debug.log != nil || m.status != "scroll log: off" {
		t.Fatalf("recorder remained active: %#v", m.debug)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []scrollLogRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record scrollLogRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("invalid JSONL %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Source != "keyboard" || records[0].Applied != 2 || records[1].Source != "wheel" || records[1].Applied != -1 {
		t.Fatalf("records=%#v", records)
	}
	if records[0].Timestamp == "" || records[0].Maximum <= 0 || records[0].Viewport != m.contentHeight() {
		t.Fatalf("missing real-time metrics: %#v", records[0])
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions info=%#v err=%v", info, err)
	}
}

func TestDebugScrollLogFailureDoesNotEnableRecorder(t *testing.T) {
	root := t.TempDir()
	notDirectory := filepath.Join(root, "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_HOME", notDirectory)
	m := &model{}
	m.handleDebugCommand("/debug", "log")
	if m.debug.log != nil || !strings.HasPrefix(m.status, "scroll log: ") || m.status == "scroll log: on" {
		t.Fatalf("state=%#v status=%q", m.debug, m.status)
	}
}

func TestDebugEnvironmentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scroll.jsonl")
	t.Setenv("GROK_SCROLL_DEBUG", "1")
	t.Setenv("GROK_FPS", "")
	t.Setenv("GROK_SCROLL_LOG", path)
	state := newDebugState()
	defer state.closeLog()
	if !state.scrollHUD || !state.fpsHUD || state.logPath != path {
		t.Fatalf("environment state=%#v", state)
	}
}
