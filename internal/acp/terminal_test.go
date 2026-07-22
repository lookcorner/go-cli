//go:build !windows

package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestACPInteractivePTYLifecycle(t *testing.T) {
	server := &Server{
		SessionDir: t.TempDir(),
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, errors.New("session factory should not be called")
		},
	}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()

	encoder := json.NewEncoder(clientToAgentW)
	messages := make(chan map[string]any, 32)
	go func() {
		decoder := json.NewDecoder(agentToClientR)
		for {
			var message map[string]any
			if err := decoder.Decode(&message); err != nil {
				close(messages)
				return
			}
			messages <- message
		}
	}()

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/terminal/pty/create",
		"params": map[string]any{
			"shell": "/bin/sh", "cwd": t.TempDir(), "rows": 24, "cols": 80, "name": "test shell",
			"env": []any{map[string]any{"name": "PTY_TEST", "value": "pty-env-value-927"}},
		},
	})
	created, _ := waitForACPResponse(t, messages, 1)
	terminalID := terminalExtensionResult(t, created)["terminalId"].(string)
	if terminalID == "" {
		t.Fatal("PTY create returned an empty terminal ID")
	}

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/terminal/pty/resize",
		"params": map[string]any{"terminalId": terminalID, "rows": 30, "cols": 100},
	})
	waitForACPResponse(t, messages, 2)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "x.ai/terminal/list", "params": map[string]any{},
	})
	listed, _ := waitForACPResponse(t, messages, 3)
	terminals := terminalExtensionResult(t, listed)["terminals"].([]any)
	if len(terminals) != 1 || terminals[0].(map[string]any)["interactive"] != true {
		t.Fatalf("unexpected PTY list response: %#v", listed)
	}

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/terminal/pty/input",
		"params": map[string]any{
			"terminalId": terminalID,
			"data":       base64.StdEncoding.EncodeToString([]byte("printf '%s' \"$PTY_TEST\"; exit\n")),
		},
	})
	var output strings.Builder
	exited := false
	deadline := time.After(5 * time.Second)
	for !exited {
		select {
		case message := <-messages:
			if message["method"] != "x.ai/terminal/pty/notification" {
				continue
			}
			params := message["params"].(map[string]any)
			if params["terminalId"] != terminalID {
				continue
			}
			switch params["type"] {
			case "output":
				data, err := base64.StdEncoding.DecodeString(params["data"].(string))
				if err != nil {
					t.Fatal(err)
				}
				output.Write(data)
			case "exit":
				exited = true
			}
		case <-deadline:
			t.Fatalf("PTY did not exit; output=%q", output.String())
		}
	}
	if !strings.Contains(output.String(), "pty-env-value-927") {
		t.Fatalf("PTY did not stream command output: %q", output.String())
	}

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "x.ai/terminal/pty/load",
		"params": map[string]any{"terminalId": terminalID},
	})
	loaded, notifications := waitForACPResponse(t, messages, 4)
	loadResult := terminalExtensionResult(t, loaded)
	if loadResult["exited"] != true || int(loadResult["rows"].(float64)) != 30 || int(loadResult["cols"].(float64)) != 100 {
		t.Fatalf("unexpected PTY load response: %#v", loaded)
	}
	var replay string
	for _, message := range notifications {
		params, _ := message["params"].(map[string]any)
		if params["type"] == "output" && params["isReplay"] == true {
			data, err := base64.StdEncoding.DecodeString(params["data"].(string))
			if err != nil {
				t.Fatal(err)
			}
			replay += string(data)
		}
	}
	if !strings.Contains(replay, "pty-env-value-927") {
		t.Fatalf("PTY load did not replay output: %q", replay)
	}

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "x.ai/terminal/kill",
		"params": map[string]any{"terminalId": terminalID},
	})
	killed, _ := waitForACPResponse(t, messages, 5)
	if terminalExtensionResult(t, killed)["outcome"] != "already_exited" {
		t.Fatalf("unexpected PTY kill outcome: %#v", killed)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "x.ai/terminal/pty/create",
		"params": map[string]any{"shell": "/bin/sh", "cwd": t.TempDir()},
	})
	running, _ := waitForACPResponse(t, messages, 6)
	runningID := terminalExtensionResult(t, running)["terminalId"].(string)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "x.ai/terminal/kill",
		"params": map[string]any{"terminalId": runningID},
	})
	terminated, _ := waitForACPResponse(t, messages, 7)
	if terminalExtensionResult(t, terminated)["outcome"] != "killed" {
		t.Fatalf("unexpected running PTY kill outcome: %#v", terminated)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "x.ai/terminal/list", "params": map[string]any{},
	})
	emptyList, _ := waitForACPResponse(t, messages, 8)
	if terminals := terminalExtensionResult(t, emptyList)["terminals"].([]any); len(terminals) != 0 {
		t.Fatalf("killed PTYs remained listed: %#v", emptyList)
	}

	_ = clientToAgentW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop after PTY test")
	}
}

func terminalExtensionResult(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	envelope := message["result"].(map[string]any)
	if envelope["error"] != nil {
		t.Fatalf("terminal extension failed: %#v", message)
	}
	result, _ := envelope["result"].(map[string]any)
	return result
}

func TestACPPipedTerminalLifecycle(t *testing.T) {
	server := &Server{
		SessionDir: t.TempDir(),
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, errors.New("session factory should not be called")
		},
	}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder := json.NewEncoder(clientToAgentW)
	messages := make(chan map[string]any, 32)
	go func() {
		decoder := json.NewDecoder(agentToClientR)
		for {
			var message map[string]any
			if err := decoder.Decode(&message); err != nil {
				close(messages)
				return
			}
			messages <- message
		}
	}()

	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/terminal/create",
		"params": map[string]any{
			"sessionId": "remote-session", "command": "/bin/sh",
			"args": []string{"-c", `printf %s "$PIPE_TEST"; exit 7`}, "cwd": t.TempDir(), "outputByteLimit": 5,
			"env": []any{map[string]any{"name": "PIPE_TEST", "value": "0123456789"}},
		},
	})
	created, _ := waitForACPResponse(t, messages, 1)
	terminalID := terminalExtensionResult(t, created)["terminalId"].(string)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/terminal/wait_for_exit",
		"params": map[string]any{"sessionId": "remote-session", "terminalId": terminalID},
	})
	waited, _ := waitForACPResponse(t, messages, 2)
	if terminalExtensionResult(t, waited)["exitCode"] != float64(7) {
		t.Fatalf("unexpected terminal exit: %#v", waited)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "x.ai/terminal/output",
		"params": map[string]any{"sessionId": "remote-session", "terminalId": terminalID},
	})
	outputMessage, _ := waitForACPResponse(t, messages, 3)
	output := terminalExtensionResult(t, outputMessage)
	if output["output"] != "56789" || output["truncated"] != true || output["exitStatus"].(map[string]any)["exitCode"] != float64(7) {
		t.Fatalf("unexpected terminal output: %#v", output)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 31, "method": "x.ai/terminal/output",
		"params": map[string]any{"sessionId": "other-session", "terminalId": terminalID},
	})
	isolated := mustACPResponse(t, messages, 31)["result"].(map[string]any)
	if isolated["result"] != nil || isolated["error"] != "terminal not found" {
		t.Fatalf("terminal crossed session boundary: %#v", isolated)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "x.ai/terminal/list", "params": map[string]any{}})
	listed, _ := waitForACPResponse(t, messages, 4)
	terminals := terminalExtensionResult(t, listed)["terminals"].([]any)
	if len(terminals) != 1 || terminals[0].(map[string]any)["interactive"] != false {
		t.Fatalf("unexpected piped terminal list: %#v", listed)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "x.ai/terminal/release",
		"params": map[string]any{"sessionId": "remote-session", "terminalId": terminalID},
	})
	terminalExtensionResult(t, mustACPResponse(t, messages, 5))
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "x.ai/terminal/output",
		"params": map[string]any{"sessionId": "remote-session", "terminalId": terminalID},
	})
	released := mustACPResponse(t, messages, 6)["result"].(map[string]any)
	if released["result"] != nil || released["error"] != "terminal not found" {
		t.Fatalf("released terminal remained accessible: %#v", released)
	}

	_ = clientToAgentW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop after piped terminal test")
	}
}

func TestACPPipedTerminalBackgroundUnblocksWait(t *testing.T) {
	manager := newTerminalManager(nil)
	defer manager.closeAll()
	id, err := manager.createCommand("session", "sleep 5", nil, t.TempDir(), nil, terminalOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() {
		_, err := manager.waitCommand("session", id)
		waited <- err
	}()
	if err := manager.backgroundCommand("session", id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err == nil || err.Error() != "terminal not found" {
			t.Fatalf("background wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("background did not unblock terminal wait")
	}
	if outcome, err := manager.killCommand("session", id); err != nil || outcome != "killed" {
		t.Fatalf("kill outcome=%q err=%v", outcome, err)
	}
}

func TestPipedTerminalSessionClosePreservesBackgroundedCommands(t *testing.T) {
	manager := newTerminalManager(nil)
	defer manager.closeAll()
	foreground, err := manager.createCommand("session", "sleep 5", nil, t.TempDir(), nil, terminalOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	background, err := manager.createCommand("session", "sleep 5", nil, t.TempDir(), nil, terminalOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.backgroundCommand("session", background); err != nil {
		t.Fatal(err)
	}
	manager.closeSessionCommands("session")
	if _, err := manager.commandOutput("session", foreground); err == nil {
		t.Fatal("foreground command survived session close")
	}
	if _, err := manager.commandOutput("session", background); err != nil {
		t.Fatalf("background command was removed on session close: %v", err)
	}
	if _, err := manager.killCommand("session", background); err != nil {
		t.Fatal(err)
	}
}

func TestPipedTerminalTruncatesAtUTF8Boundary(t *testing.T) {
	terminal := &commandTerminal{limit: 4}
	terminal.record([]byte("a你好"))
	if output := string(terminal.output); output != "好" || !terminal.truncated {
		t.Fatalf("output=%q truncated=%v", output, terminal.truncated)
	}
}

func mustACPResponse(t *testing.T, messages <-chan map[string]any, id int) map[string]any {
	t.Helper()
	message, _ := waitForACPResponse(t, messages, id)
	return message
}

func waitForACPResponse(t *testing.T, messages <-chan map[string]any, id int) (map[string]any, []map[string]any) {
	t.Helper()
	var notifications []map[string]any
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case message, ok := <-messages:
			if !ok {
				t.Fatal("ACP output closed before response")
			}
			if value, ok := message["id"].(float64); ok && int(value) == id {
				return message, notifications
			}
			notifications = append(notifications, message)
		case <-timer.C:
			t.Fatalf("timed out waiting for ACP response %d", id)
		}
	}
}

func TestPTYReplayBufferIsBounded(t *testing.T) {
	terminal := &ptyTerminal{}
	terminal.recordOutput([]byte(strings.Repeat("a", terminalOutputBytes+100)))
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if len(terminal.output) != terminalOutputBytes || terminal.outputOffset != terminalOutputBytes+100 {
		t.Fatalf("unexpected bounded output: len=%d offset=%d", len(terminal.output), terminal.outputOffset)
	}
}

func TestPTYActivityTransitions(t *testing.T) {
	events := make(chan string, 16)
	manager := newTerminalManager(func(method string, params any) {
		if method != "x.ai/terminal/pty/notification" {
			return
		}
		typeName, _ := params.(map[string]any)["type"].(string)
		if typeName == "process_started" || typeName == "process_ended" {
			events <- typeName
		}
	})
	id, err := manager.create("/bin/sh", t.TempDir(), nil, 24, 80, "activity")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.closeAll()

	select {
	case event := <-events:
		t.Fatalf("idle shell emitted %q", event)
	case <-time.After(terminalActivityInterval * 3):
	}
	if _, err := manager.load(id); err != nil {
		t.Fatal(err)
	}
	waitForPTYActivity(t, events, "process_ended")
	if err := manager.writeInput(id, []byte("sleep 2\n")); err != nil {
		t.Fatal(err)
	}
	waitForPTYActivity(t, events, "process_started")
	waitForPTYActivity(t, events, "process_ended")
	select {
	case event := <-events:
		t.Fatalf("steady idle state repeated %q", event)
	case <-time.After(terminalActivityInterval * 2):
	}
}

func waitForPTYActivity(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("activity event=%q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}
