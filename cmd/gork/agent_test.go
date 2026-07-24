package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestNormalizeAgentStdioArgs(t *testing.T) {
	got, err := normalizeAgentStdioArgs([]string{
		"--model", "grok-4", "--effort", "high", "--yolo", "--no-leader",
		"--workspace", "/project", "--session-dir", "/sessions", "--no-plan", "stdio",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--acp", "--model", "grok-4", "--effort", "high", "--always-approve",
		"--workspace", "/project", "--session-dir", "/sessions", "--no-plan",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("normalized=%q want=%q", got, want)
	}
	got, err = normalizeAgentStdioArgs([]string{"stdio", "--model=grok-4", "--permission-mode=deny"})
	if err != nil || strings.Join(got, " ") != "--acp --model=grok-4 --permission-mode=deny" {
		t.Fatalf("equals normalized=%q err=%v", got, err)
	}
}

func TestAgentRejectsUnimplementedModesAndOptions(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "headless mode"},
		{[]string{"headless"}, "headless mode"},
		{[]string{"serve"}, "serve mode"},
		{[]string{"leader"}, "leader mode"},
		{[]string{"--leader", "stdio"}, "leader connection"},
		{[]string{"--plugin-dir", "/tmp/plugin", "stdio"}, "not implemented"},
		{[]string{"--model=", "stdio"}, "unknown agent option"},
		{[]string{"unknown"}, "unknown agent mode"},
	} {
		if _, err := normalizeAgentStdioArgs(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args=%v err=%v want=%q", test.args, err, test.want)
		}
	}
}

func TestAgentHelp(t *testing.T) {
	if _, err := normalizeAgentStdioArgs([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatal("help sentinel was not returned")
	}
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"agent", "--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Usage: gork agent") {
		t.Fatalf("help=%q", stderr.String())
	}
}

func TestAgentStdioRunsACPInitialize(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GORK_API_KEY", "test-key")
	root := t.TempDir()
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"agent", "--model", "test-model", "--workspace", root, "stdio"}, strings.NewReader(request), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err != nil {
		t.Fatalf("decode initialize response: %v\n%s", err, stdout.String())
	}
	if response.ID != 1 || response.Result.ProtocolVersion != 1 {
		t.Fatalf("response=%#v", response)
	}
}
