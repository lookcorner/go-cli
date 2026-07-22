package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestParseClientGitHead(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`{}`, false},
		{`{"clientCapabilities":{"_meta":{"x.ai/gitHeadChanged":true}}}`, true},
		{`{"clientCapabilities":{"_meta":{"x.ai/gitHeadChanged":false}}}`, false},
		{`{"clientCapabilities":{"_meta":{"x.ai/gitHeadChanged":{}}}}`, false},
		{`{`, false},
	} {
		if got := parseClientGitHead([]byte(test.raw)); got != test.want {
			t.Errorf("parseClientGitHead(%q)=%v want %v", test.raw, got, test.want)
		}
	}
}

func TestGitHeadChangedInitialDedupAndToolRefresh(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "baseline")
	branch := strings.TrimSpace(runACPGitOutput(t, root, "branch", "--show-current"))

	output := &synchronizedBuffer{}
	current := &session{id: "session-1", ctx: context.Background(), cwd: root, gitHeadEnabled: true}
	server := &Server{output: output, sessions: map[string]*session{"session-1": current}}
	server.notifyGitHead(current)
	server.notifyGitHead(current)
	messages := decodeACPBytes(t, output.snapshot())
	if len(messages) != 1 {
		t.Fatalf("initial notifications=%#v", messages)
	}
	params := messages[0]["params"].(map[string]any)
	if messages[0]["method"] != "x.ai/git_head_changed" || params["sessionId"] != "session-1" || params["branch"] != branch || params["isWorktree"] != false || params["mainRepo"] != nil {
		t.Fatalf("initial notification=%#v", messages[0])
	}

	runACPGit(t, root, "checkout", "-qb", "feature/tool-refresh")
	observer := &sessionToolObserver{server: server, sessionID: current.id}
	observer.ToolFinished(api.ToolCall{CallID: "call-1", Name: "shell"}, tools.ExecutionResult{Output: "done"}, nil)
	messages = decodeACPBytes(t, output.snapshot())
	if len(messages) != 3 || messages[2]["method"] != "x.ai/git_head_changed" || messages[2]["params"].(map[string]any)["branch"] != "feature/tool-refresh" {
		t.Fatalf("tool notifications=%#v", messages)
	}

	runACPGit(t, root, "checkout", "-qb", "feature/failed-tool")
	observer.ToolFinished(api.ToolCall{CallID: "call-2", Name: "shell"}, tools.ExecutionResult{}, errors.New("failed"))
	observer.ToolFinished(api.ToolCall{CallID: "call-3", Name: "read_file"}, tools.ExecutionResult{}, nil)
	messages = decodeACPBytes(t, output.snapshot())
	for _, message := range messages[3:] {
		if message["method"] == "x.ai/git_head_changed" {
			t.Fatalf("failed or read-only tool refreshed git head: %#v", messages)
		}
	}

	runACPGit(t, root, "checkout", "--detach", "-q")
	observer.ToolFinished(api.ToolCall{CallID: "call-4", Name: "search_replace"}, tools.ExecutionResult{Output: "done"}, nil)
	messages = decodeACPBytes(t, output.snapshot())
	last := messages[len(messages)-1]
	if last["method"] != "x.ai/git_head_changed" || last["params"].(map[string]any)["branch"] != nil {
		t.Fatalf("detached head notification=%#v", last)
	}
}

func TestGitHeadChangedWorktreePayload(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "baseline")
	linked := filepath.Join(t.TempDir(), "linked")
	runACPGit(t, root, "worktree", "add", "-qb", "feature/linked", linked)

	output := &synchronizedBuffer{}
	current := &session{id: "linked-session", ctx: context.Background(), cwd: linked, gitHeadEnabled: true}
	server := &Server{output: output}
	server.notifyGitHead(current)
	messages := decodeACPBytes(t, output.snapshot())
	params := messages[0]["params"].(map[string]any)
	if params["branch"] != "feature/linked" || params["isWorktree"] != true || params["mainRepo"] == nil || params["mainRepo"] == "" {
		t.Fatalf("worktree notification=%#v", messages[0])
	}
}

func TestStartGitHeadNotificationsOwnsWatcherLifecycle(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &synchronizedBuffer{}
	current := &session{id: "session-1", ctx: ctx, cwd: root, gitHeadEnabled: true}
	server := &Server{output: output}
	server.startGitHeadNotifications(current)
	current.mu.Lock()
	watchCancel, watchDone := current.gitWatchCancel, current.gitWatchDone
	current.mu.Unlock()
	if watchCancel == nil || watchDone == nil || len(decodeACPBytes(t, output.snapshot())) != 1 {
		t.Fatal("git head watcher or initial notification missing")
	}
	server.startGitHeadNotifications(current)
	current.mu.Lock()
	secondCancel, secondDone := current.gitWatchCancel, current.gitWatchDone
	current.mu.Unlock()
	if secondCancel == nil || secondDone != watchDone || len(decodeACPBytes(t, output.snapshot())) != 1 {
		t.Fatal("starting Git HEAD notifications twice replaced the active watcher")
	}
	watchCancel()
	select {
	case <-watchDone:
	case <-time.After(3 * time.Second):
		t.Fatal("git head watcher did not stop")
	}
}

func TestGitHeadCapabilityIsStrictlyOptIn(t *testing.T) {
	nonRepo := t.TempDir()
	current := &session{id: "session-1", ctx: context.Background(), cwd: nonRepo}
	output := &synchronizedBuffer{}
	(&Server{output: output}).notifyGitHead(current)
	if len(output.snapshot()) != 0 {
		t.Fatalf("notification emitted without opt-in: %s", output.snapshot())
	}
	current.gitHeadEnabled = true
	(&Server{output: output}).notifyGitHead(current)
	if len(output.snapshot()) != 0 {
		t.Fatalf("non-Git workspace emitted a notification: %s", output.snapshot())
	}

	runACPGit(t, nonRepo, "init", "-q")
	current.closed = true
	(&Server{output: output}).notifyGitHead(current)
	if len(output.snapshot()) != 0 {
		t.Fatalf("closed session emitted a notification: %s", output.snapshot())
	}

	var payload gitHeadChanged
	encoded, err := json.Marshal(gitHeadChanged{SessionID: "s"})
	if err != nil || json.Unmarshal(encoded, &payload) != nil || payload.Branch != nil || payload.MainRepo != nil {
		t.Fatalf("nullable payload mismatch: %s err=%v", encoded, err)
	}
}

func TestGitHeadHelpers(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := displayHomePath(filepath.Join(home, "project")); got != filepath.Join("~", "project") {
		t.Fatalf("home display path=%q", got)
	}
	for _, name := range []string{"write_file", "edit_file", "search_replace", "hashline_edit", "shell", "run_terminal_cmd"} {
		if !gitChangingTool(name) {
			t.Errorf("%s should refresh Git HEAD", name)
		}
	}
	if gitChangingTool("read_file") {
		t.Fatal("read_file should not refresh Git HEAD")
	}
}
