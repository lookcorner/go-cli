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
	terminalID := created["result"].(map[string]any)["terminalId"].(string)
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
	terminals := listed["result"].(map[string]any)["terminals"].([]any)
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
	loadResult := loaded["result"].(map[string]any)
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
	if killed["result"].(map[string]any)["outcome"] != "already_exited" {
		t.Fatalf("unexpected PTY kill outcome: %#v", killed)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "x.ai/terminal/pty/create",
		"params": map[string]any{"shell": "/bin/sh", "cwd": t.TempDir()},
	})
	running, _ := waitForACPResponse(t, messages, 6)
	runningID := running["result"].(map[string]any)["terminalId"].(string)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "x.ai/terminal/kill",
		"params": map[string]any{"terminalId": runningID},
	})
	terminated, _ := waitForACPResponse(t, messages, 7)
	if terminated["result"].(map[string]any)["outcome"] != "killed" {
		t.Fatalf("unexpected running PTY kill outcome: %#v", terminated)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "x.ai/terminal/list", "params": map[string]any{},
	})
	emptyList, _ := waitForACPResponse(t, messages, 8)
	if terminals := emptyList["result"].(map[string]any)["terminals"].([]any); len(terminals) != 0 {
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
