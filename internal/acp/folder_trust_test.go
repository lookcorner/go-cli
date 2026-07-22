package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseClientFolderTrust(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`{}`, false},
		{`{"clientCapabilities":{"_meta":{"x.ai/folderTrust":{"interactive":true}}}}`, true},
		{`{"clientCapabilities":{"_meta":{"x.ai/folderTrust":{"interactive":false}}}}`, false},
		{`{"clientCapabilities":{"_meta":{"x.ai/folderTrust":true}}}`, false},
		{`{`, false},
	} {
		if got := parseClientFolderTrust([]byte(test.raw)); got != test.want {
			t.Errorf("parseClientFolderTrust(%q)=%v want %v", test.raw, got, test.want)
		}
	}
}

func TestFolderTrustPromptGrantsAndReloadsWorkspace(t *testing.T) {
	root := folderTrustFixture(t)
	reloaded := make(chan struct{}, 1)
	current := &session{id: "session-trust", ctx: context.Background(), cwd: root, runner: &agent.Runner{
		UpdatePlugins: func(context.Context, func(*plugin.Settings)) ([]plugin.Plugin, error) {
			reloaded <- struct{}{}
			return nil, nil
		},
	}}
	output := &synchronizedBuffer{}
	server := &Server{
		FolderTrustEnabled: true, clientFolderTrust: true, output: output,
		sessions: map[string]*session{current.id: current}, pendingTrust: make(map[string]chan folderTrustResult),
	}
	server.startFolderTrustPrompt(current)
	request := waitFolderTrustRequest(t, output, 1)
	params := request["params"].(map[string]any)
	kinds := params["configKinds"].([]any)
	if request["method"] != "x.ai/folder_trust/request" || params["sessionId"] != current.id || params["cwd"] != root || params["workspace"] != workspace.WorkspaceTrustKey(root) || len(kinds) != 1 || kinds[0] != "mcp" {
		t.Fatalf("folder trust request=%#v", request)
	}
	server.handleClientResponse(message{ID: rawMessageID(t, request["id"]), Result: json.RawMessage(`{"outcome":"trust"}`)})
	server.wg.Wait()
	select {
	case <-reloaded:
	default:
		t.Fatal("trusted workspace was not reloaded")
	}
	if workspace.ResolveFolderTrust(root, true, false) != workspace.TrustTrusted {
		t.Fatal("folder trust grant was not persisted")
	}
}

func TestFolderTrustPromptRejectsOnceAndCancellationCanRetry(t *testing.T) {
	t.Run("reject is fail closed and deduplicated", func(t *testing.T) {
		root := folderTrustFixture(t)
		output := &synchronizedBuffer{}
		current := &session{id: "session-reject", ctx: context.Background(), cwd: root, runner: &agent.Runner{}}
		server := &Server{
			FolderTrustEnabled: true, clientFolderTrust: true, output: output,
			sessions: map[string]*session{current.id: current}, pendingTrust: make(map[string]chan folderTrustResult),
		}
		server.startFolderTrustPrompt(current)
		request := waitFolderTrustRequest(t, output, 1)
		server.maybePromptFolderTrust(&session{id: "session-sibling", ctx: context.Background(), cwd: root, runner: &agent.Runner{}})
		if messages := decodeACPBytes(t, output.snapshot()); len(messages) != 1 {
			t.Fatalf("concurrent workspace prompts were not deduplicated: %#v", messages)
		}
		server.handleClientResponse(message{ID: rawMessageID(t, request["id"]), Result: json.RawMessage(`{"outcome":"unexpected"}`)})
		server.wg.Wait()
		if workspace.ResolveFolderTrust(root, true, false) != workspace.TrustUntrusted {
			t.Fatal("unknown outcome granted folder trust")
		}
		server.startFolderTrustPrompt(current)
		server.wg.Wait()
		if messages := decodeACPBytes(t, output.snapshot()); len(messages) != 1 {
			t.Fatalf("rejected workspace was prompted again: %#v", messages)
		}
	})

	t.Run("cancel releases dedup key", func(t *testing.T) {
		root := folderTrustFixture(t)
		output := &synchronizedBuffer{}
		ctx, cancel := context.WithCancel(context.Background())
		current := &session{id: "session-cancel", ctx: ctx, cwd: root, runner: &agent.Runner{}}
		server := &Server{
			FolderTrustEnabled: true, clientFolderTrust: true, output: output,
			sessions: map[string]*session{current.id: current}, pendingTrust: make(map[string]chan folderTrustResult),
		}
		server.startFolderTrustPrompt(current)
		waitFolderTrustRequest(t, output, 1)
		cancel()
		server.wg.Wait()

		current.ctx = context.Background()
		server.startFolderTrustPrompt(current)
		request := waitFolderTrustRequest(t, output, 2)
		server.handleClientResponse(message{ID: rawMessageID(t, request["id"]), Result: json.RawMessage(`{"outcome":"reject"}`)})
		server.wg.Wait()
	})
}

func folderTrustFixture(t *testing.T) string {
	t.Helper()
	previousVersion := version.Current
	version.Current = "1.0.0"
	t.Cleanup(func() { version.Current = previousVersion })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func waitFolderTrustRequest(t *testing.T, output *synchronizedBuffer, count int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if messages := decodeACPBytes(t, output.snapshot()); len(messages) >= count {
			return messages[count-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for folder trust request %d", count)
	return nil
}

func rawMessageID(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
