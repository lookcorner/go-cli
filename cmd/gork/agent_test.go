package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestNormalizeAgentArgs(t *testing.T) {
	t.Setenv("GROK_AGENT_SECRET", "environment-token")
	got, server, err := normalizeAgentArgs([]string{
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
	if server == nil || server.mode != "stdio" || !server.noLeader {
		t.Fatalf("stdio options=%+v", server)
	}
	got, server, err = normalizeAgentArgs([]string{"stdio", "--model=grok-4", "--permission-mode=deny"})
	if err != nil || strings.Join(got, " ") != "--acp --model=grok-4 --permission-mode=deny" {
		t.Fatalf("equals normalized=%q err=%v", got, err)
	}
	got, server, err = normalizeAgentArgs([]string{"--bind=127.0.0.1:0", "--secret", "token", "serve"})
	if err != nil || server == nil || server.bind != "127.0.0.1:0" || server.secret != "token" || strings.Join(got, " ") != "--acp" {
		t.Fatalf("serve normalized=%q options=%+v err=%v", got, server, err)
	}
	_, server, err = normalizeAgentArgs([]string{"serve"})
	if err != nil || server == nil || server.secret != "environment-token" {
		t.Fatalf("environment secret options=%+v err=%v", server, err)
	}
	got, server, err = normalizeAgentArgs([]string{"--no-exit-on-disconnect", "leader"})
	if err != nil || server == nil || server.mode != "leader" || !server.noExitOnDisconnect || strings.Join(got, " ") != "--acp" {
		t.Fatalf("leader normalized=%q options=%+v err=%v", got, server, err)
	}
	got, server, err = normalizeAgentArgs([]string{"--leader", "stdio"})
	if err != nil || server == nil || !server.forceLeader || strings.Join(got, " ") != "--acp" {
		t.Fatalf("follower normalized=%q options=%+v err=%v", got, server, err)
	}
	got, server, err = normalizeAgentArgs([]string{"--plugin-dir", "one", "--plugin-dir=two", "--no-leader", "stdio"})
	if err != nil || strings.Join(got, " ") != "--acp --plugin-dir one --plugin-dir=two" || strings.Join(server.pluginDirs, "|") != "one|two" {
		t.Fatalf("plugin dirs normalized=%q options=%+v err=%v", got, server, err)
	}
	got, _, err = normalizeAgentArgs([]string{
		"--cli-chat-proxy-base-url", "https://proxy.example/v1/",
		"--xai-api-base-url=https://api.example/v1/", "serve",
	})
	if err != nil || strings.Join(got, " ") != "--acp --cli-chat-proxy-base-url https://proxy.example/v1/ --base-url=https://api.example/v1/" {
		t.Fatalf("endpoint overrides normalized=%q err=%v", got, err)
	}
}

func TestAgentRejectsUnimplementedModesAndOptions(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "headless mode"},
		{[]string{"headless"}, "headless mode"},
		{[]string{"--leader", "--no-leader", "stdio"}, "cannot be used together"},
		{[]string{"--leader", "leader"}, "require agent stdio"},
		{[]string{"--plugin-dir", "stdio"}, "headless mode"},
		{[]string{"--bind", "127.0.0.1:0", "stdio"}, "require agent serve"},
		{[]string{"--no-exit-on-disconnect", "stdio"}, "requires agent leader"},
		{[]string{"--model=", "stdio"}, "unknown agent option"},
		{[]string{"unknown"}, "unknown agent mode"},
	} {
		if _, _, err := normalizeAgentArgs(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args=%v err=%v want=%q", test.args, err, test.want)
		}
	}
}

func TestCanonicalAgentPluginDirsSkipsInvalidPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugin")
	file := filepath.Join(root, "file")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	got := canonicalAgentPluginDirs([]string{dir, file, filepath.Join(root, "missing")}, &stderr)
	canonical, _ := filepath.EvalSymlinks(dir)
	if strings.Join(got, "|") != canonical {
		t.Fatalf("plugin dirs=%q", got)
	}
	if output := stderr.String(); !strings.Contains(output, "not a directory; skipping") || !strings.Contains(output, "missing") {
		t.Fatalf("stderr=%q", output)
	}
}

func TestAgentUsesLeaderOnlyForLeaderAndStdioModes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[cli]\nuse_leader = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--acp", "--config", configPath}
	if !agentUsesLeader(agentServerOptions{mode: "stdio"}, args) {
		t.Fatal("stdio ignored configured leader")
	}
	if agentUsesLeader(agentServerOptions{mode: "serve"}, args) {
		t.Fatal("serve incorrectly used configured leader")
	}
	if !agentUsesLeader(agentServerOptions{mode: "leader"}, args) {
		t.Fatal("leader mode was not recognized")
	}
}

func TestAgentHelp(t *testing.T) {
	if _, _, err := normalizeAgentArgs([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
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

func TestAgentWebSocketAuthentication(t *testing.T) {
	requireLoopback(t)
	relay := newAgentWebSocketRelay()
	defer relay.Close()
	server := httptest.NewServer(agentWebSocketHandler("test-secret", relay))
	defer server.Close()

	response, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "Invalid or missing") {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}

	for _, authorize := range []func(*websocket.Config){
		func(config *websocket.Config) { config.Header.Set("Authorization", "Bearer test-secret") },
		func(config *websocket.Config) {
			config.Location.RawQuery = "server-key=test-secret"
		},
	} {
		config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "http://localhost")
		if err != nil {
			t.Fatal(err)
		}
		authorize(config)
		connection, err := websocket.DialConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
	}
}

func TestAgentWebSocketRelayAcceptsBinaryAndReconnects(t *testing.T) {
	requireLoopback(t)
	relay := newAgentWebSocketRelay()
	defer relay.Close()
	server := httptest.NewServer(agentWebSocketHandler("test-secret", relay))
	defer server.Close()

	go func() {
		decoder := json.NewDecoder(relay)
		encoder := json.NewEncoder(relay)
		requests := 0
		for {
			var request map[string]any
			if decoder.Decode(&request) != nil {
				return
			}
			requests++
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"requests": requests}})
		}
	}()

	dial := func() *websocket.Conn {
		t.Helper()
		config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?server-key=test-secret", "http://localhost")
		if err != nil {
			t.Fatal(err)
		}
		connection, err := websocket.DialConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		return connection
	}
	roundTrip := func(connection *websocket.Conn, request any, wantRequests int) {
		t.Helper()
		if err := websocket.Message.Send(connection, request); err != nil {
			t.Fatal(err)
		}
		var response string
		if err := websocket.Message.Receive(connection, &response); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(response, fmt.Sprintf(`"requests":%d`, wantRequests)) {
			t.Fatalf("response=%q", response)
		}
	}

	first := dial()
	roundTrip(first, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, 1)
	_ = first.Close()
	second := dial()
	defer second.Close()
	roundTrip(second, []byte(`{"jsonrpc":"2.0","id":2,"method":"session/list"}`), 2)
}

func TestRandomAgentServerSecret(t *testing.T) {
	first, err := randomAgentServerSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomAgentServerSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 12 || len(second) != 12 || first == second {
		t.Fatalf("first=%q second=%q", first, second)
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
