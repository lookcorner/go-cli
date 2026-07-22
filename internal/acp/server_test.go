package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/marketplace"
	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/plugin"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/subagent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestGitExtensionWireContract(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "baseline")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output}
	server.handleGit(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/git/status", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	extension := response["result"].(map[string]any)
	status := extension["result"].(map[string]any)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if status["root"] != resolvedRoot || len(status["unstaged"].([]any)) != 1 || extension["error"] != nil {
		t.Fatalf("unexpected status wire response: %#v", response)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/git/current_commit", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if result := response["result"].(map[string]any)["result"]; result != strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD")) {
		t.Fatalf("unexpected current commit response: %#v", response)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/git/git_repo_root", Params: json.RawMessage(`{"currentWorkingDirectory":` + strconv.Quote(root) + `}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if got := response["result"].(map[string]any)["GitRepo"].(map[string]any)["gitRoot"]; got != resolvedRoot {
		t.Fatalf("unexpected repo root response: %#v", response)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/git/info", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if info := response["result"].(map[string]any)["result"].(map[string]any); info["vcsKind"] != "git" || info["root"] != resolvedRoot {
		t.Fatalf("unexpected git info response: %#v", response)
	}
	output.Reset()
	head := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD"))
	server.handleGit(context.Background(), message{ID: json.RawMessage("5"), Method: "x.ai/git/checkout_commit", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `,"commit":` + strconv.Quote(head) + `}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if result := response["result"].(map[string]any); result["checked_out"] != true || result["fetched"] != false {
		t.Fatalf("unexpected checkout commit response: %#v", response)
	}
	output.Reset()
	runACPGit(t, root, "add", "tracked.txt")
	server.handleGit(context.Background(), message{ID: json.RawMessage("6"), Method: "x.ai/git/commit", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `,"message":"wire commit"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	commitResult := response["result"].(map[string]any)
	if data := commitResult["result"].(map[string]any); data["commitHash"] == "" || !strings.HasPrefix(data["output"].(string), "Committed: ") || commitResult["error"] != nil {
		t.Fatalf("unexpected commit response: %#v", response)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("7"), Method: "x.ai/git/files", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `,"paths":["tracked.txt"],"version":"HEAD"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	files := response["result"].(map[string]any)["result"].(map[string]any)["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["content"] != "changed\n" {
		t.Fatalf("unexpected git files response: %#v", response)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("8"), Method: "x.ai/git/stage/content", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `,"path":"tracked.txt","content":"index only\n"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if got := runACPGitOutput(t, root, "show", ":tracked.txt"); got != "index only\n" {
		t.Fatalf("stage content index=%q", got)
	}
	output.Reset()
	server.handleGit(context.Background(), message{ID: json.RawMessage("9"), Method: "x.ai/git/diffs", Params: json.RawMessage(`{"gitRoot":` + strconv.Quote(root) + `,"from":"HEAD","to":"staged","includePatch":true,"includeContent":true}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	diffFiles := response["result"].(map[string]any)["result"].(map[string]any)["files"].([]any)
	if len(diffFiles) != 1 || diffFiles[0].(map[string]any)["newText"] != "index only\n" || !strings.Contains(diffFiles[0].(map[string]any)["patch"].(string), "+index only") {
		t.Fatalf("unexpected git diffs response: %#v", response)
	}
}

func TestPlanModeExitApprovalWireContract(t *testing.T) {
	tests := []tools.PlanModeDecision{
		{Outcome: "approved"},
		{Outcome: "cancelled", Feedback: "add rollback steps"},
	}
	for _, expected := range tests {
		t.Run(expected.Outcome, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			server := &Server{output: writer, pendingPlan: make(map[string]chan planApprovalResult)}
			type result struct {
				decision tools.PlanModeDecision
				err      error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			go func() {
				decision, err := server.RequestPlanModeExit(ctx, "sess-1", tools.PlanModeEvent{
					ToolCallID: "call-1", PlanContent: "# Plan",
				})
				done <- result{decision: decision, err: err}
			}()

			var request message
			if err := json.NewDecoder(reader).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Method != "x.ai/exit_plan_mode" {
				t.Fatalf("method=%q", request.Method)
			}
			var params map[string]any
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params["sessionId"] != "sess-1" || params["toolCallId"] != "call-1" || params["planContent"] != "# Plan" {
				t.Fatalf("params=%#v", params)
			}
			encoded, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			server.handleClientResponse(message{ID: request.ID, Result: encoded})
			got := <-done
			if got.err != nil || got.decision != expected {
				t.Fatalf("decision=%#v err=%v", got.decision, got.err)
			}
			_ = writer.Close()
		})
	}
}

func TestCheckoutSessionHeadWireContract(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "first")
	first := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "commit", "-qam", "second")

	sessionDir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "checkout-session")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": root, "headCommit": first})
	_ = logger.Close()
	var output bytes.Buffer
	server := &Server{SessionDir: sessionDir, output: &output}
	server.handleGit(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/git/checkout_session_head", Params: json.RawMessage(`{"sessionId":"checkout-session"}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if result := response["result"].(map[string]any); result["checked_out"] != true || result["fetched"] != false {
		t.Fatalf("unexpected checkout session HEAD response: %#v", response)
	}
	if got := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD")); got != first {
		t.Fatalf("HEAD=%q want=%q", got, first)
	}
}

func TestFSExtensionWireContract(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "read.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"fs-session": {id: "fs-session", cwd: root}}}
	call := func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		output.Reset()
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		server.handleFS(message{ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: data})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	listed := call(1, "x.ai/fs/list", map[string]any{"sessionId": "fs-session", "path": ".", "depth": 1})
	extension := listed["result"].(map[string]any)
	nodes := extension["result"].(map[string]any)["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["name"] != "read.txt" || extension["error"] != nil {
		t.Fatalf("unexpected list response: %#v", listed)
	}
	read := call(2, "x.ai/fs/read_file", map[string]any{"sessionId": "fs-session", "path": "read.txt"})
	if result := read["result"].(map[string]any)["result"].(map[string]any); result["content"] != "hello\n" || result["lineCount"].(float64) != 1 {
		t.Fatalf("unexpected read response: %#v", read)
	}
	write := call(3, "x.ai/fs/write_file", map[string]any{"sessionId": "fs-session", "path": "nested/new.txt", "content": "new"})
	if write["result"].(map[string]any)["error"] != nil {
		t.Fatalf("unexpected write response: %#v", write)
	}
	exists := call(4, "x.ai/fs/exists", map[string]any{"sessionId": "fs-session", "path": "nested/new.txt"})
	if exists["result"].(map[string]any)["result"].(map[string]any)["exists"] != true {
		t.Fatalf("unexpected exists response: %#v", exists)
	}
	deleted := call(5, "x.ai/fs/delete_file", map[string]any{"sessionId": "fs-session", "path": "nested/new.txt"})
	if deleted["result"].(map[string]any)["error"] != nil {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}
	missingSession := call(6, "x.ai/fs/exists", map[string]any{"path": "read.txt"})
	if missingSession["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("relative path did not require session: %#v", missingSession)
	}
}

func TestSessionAdminExtensionWireContract(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "admin-session")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "test-model"})
	_ = logger.Append("user_prompt", map[string]any{"text": "searchable prompt"})
	_ = logger.Append("model_response", map[string]any{"text": "answer", "response_id": "r1", "tool_call_count": 0})
	_ = logger.Close()
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output, sessions: make(map[string]*session)}
	server.handleSessionAdmin(message{ID: json.RawMessage("1"), Method: "x.ai/session/rename", Params: json.RawMessage(`{"sessionId":"admin-session","title":"Manual title"}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["result"].(map[string]any)["success"] != true {
		t.Fatalf("unexpected rename response: %#v", response)
	}
	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("2"), Method: "x.ai/session/info", Params: json.RawMessage(`{"sessionId":"admin-session"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	info := response["result"].(map[string]any)["result"].(map[string]any)
	if info["sessionId"] != "admin-session" || info["cwd"] != cwd || info["model"] != "test-model" || info["turns"].(float64) != 1 {
		t.Fatalf("unexpected info response: %#v", response)
	}
	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("3"), Method: "x.ai/session/search", Params: json.RawMessage(`{"query":"searchable","includeContent":true}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	results := response["result"].(map[string]any)["result"].(map[string]any)["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["sessionId"] != "admin-session" {
		t.Fatalf("unexpected search response: %#v", response)
	}
	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("4"), Method: "x.ai/prompt_history", Params: json.RawMessage(`{"cwd":` + strconv.Quote(cwd) + `,"session_id":"admin-session"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	prompts := response["result"].(map[string]any)["prompts"].([]any)
	if len(prompts) != 1 || prompts[0] != "searchable prompt" {
		t.Fatalf("unexpected prompt history response: %#v", response)
	}
	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("5"), Method: "x.ai/session/delete", Params: json.RawMessage(`{"sessionId":"admin-session"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["result"].(map[string]any)["success"] != true {
		t.Fatalf("unexpected delete response: %#v", response)
	}
	if _, err := os.Stat(logger.Path()); !os.IsNotExist(err) {
		t.Fatalf("session survived ACP delete: %v", err)
	}
}

func TestSessionSummariesWireContract(t *testing.T) {
	dir, firstCWD, secondCWD := t.TempDir(), t.TempDir(), t.TempDir()
	for _, fixture := range []struct {
		id, cwd, prompt string
	}{{"summary-one", firstCWD, "First summary"}, {"summary-two", secondCWD, "Second summary"}} {
		logger, err := sessionlog.NewLoggerWithID(dir, fixture.id)
		if err != nil {
			t.Fatal(err)
		}
		_ = logger.Append("session_metadata", map[string]any{"cwd": fixture.cwd, "modelId": "test-model"})
		_ = logger.Append("user_prompt", map[string]any{"text": fixture.prompt})
		_ = logger.Close()
	}
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output}
	call := func(id int, method, params string) map[string]any {
		t.Helper()
		output.Reset()
		server.handleSessionSummaries(message{ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: json.RawMessage(params)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	listed := call(1, "x.ai/session_summaries/session_list", `{"workspace_directory":`+strconv.Quote(firstCWD)+`}`)
	summaries := listed["result"].(map[string]any)["session_summaries"].([]any)
	if len(summaries) != 1 || summaries[0].(map[string]any)["session_summary"] != "First summary" {
		t.Fatalf("unexpected session summaries: %#v", listed)
	}
	overview := call(2, "x.ai/session_summaries/workspace_list", `{}`)
	all := overview["result"].(map[string]any)["all_sessions"].(map[string]any)
	if len(all[firstCWD].([]any)) != 1 || len(all[secondCWD].([]any)) != 1 {
		t.Fatalf("unexpected workspace summaries: %#v", overview)
	}
	recent := call(3, "x.ai/session_summaries/workspace_list_recent", `{"limit":1}`)
	if rows := recent["result"].([]any); len(rows) != 1 {
		t.Fatalf("unexpected recent summaries: %#v", recent)
	}
}

func TestSessionRosterWireContract(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	for _, id := range []string{"live-session", "dormant-session"} {
		logger, err := sessionlog.NewLoggerWithID(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		_ = logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "stored-model"})
		_ = logger.Append("user_prompt", map[string]any{"text": id + " title"})
		_ = logger.Close()
	}
	var output bytes.Buffer
	server := &Server{
		SessionDir: dir, output: &output,
		sessions: map[string]*session{"live-session": {
			id: "live-session", cwd: cwd, runner: &agent.Runner{Model: "live-model"}, running: true, updated: time.Now().UTC(),
		}},
	}
	server.handleSessionRoster(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/sessions/list", Params: json.RawMessage(`{}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	extension := response["result"].(map[string]any)
	rows := extension["result"].(map[string]any)["sessions"].([]any)
	if len(rows) != 2 || extension["error"] != nil {
		t.Fatalf("unexpected roster response: %#v", response)
	}
	byID := make(map[string]map[string]any)
	for _, row := range rows {
		item := row.(map[string]any)
		byID[item["sessionId"].(string)] = item
	}
	if live := byID["live-session"]; live["activity"] != "working" || live["resident"] != true || live["modelId"] != "live-model" {
		t.Fatalf("unexpected live roster row: %#v", live)
	}
	if dormant := byID["dormant-session"]; dormant["activity"] != "dormant" || dormant["resident"] != false {
		t.Fatalf("unexpected dormant roster row: %#v", dormant)
	}
}

func TestUnifiedSessionListWireContract(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	for _, id := range []string{"list-one", "list-two", "list-three"} {
		logger, err := sessionlog.NewLoggerWithID(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		_ = logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "test-model"})
		_ = logger.Append("user_prompt", map[string]any{"text": id + " title"})
		_ = logger.Close()
	}
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output}
	call := func(id int, params map[string]any) map[string]any {
		t.Helper()
		output.Reset()
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		server.handleUnifiedSessionList(message{ID: json.RawMessage(strconv.Itoa(id)), Method: "x.ai/session/list", Params: data})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := call(1, map[string]any{"cwd": cwd, "limit": 2})
	extension := first["result"].(map[string]any)
	result := extension["result"].(map[string]any)
	rows := result["sessions"].([]any)
	cursor := result["nextCursor"].(string)
	if len(rows) != 2 || cursor == "" || extension["error"] != nil {
		t.Fatalf("unexpected first session page: %#v", first)
	}
	meta := result["_meta"].(map[string]any)
	if meta["x.ai/partial"].(map[string]any)["conversations"] != false || len(meta["x.ai/facets"].(map[string]any)["keys"].([]any)) != 2 {
		t.Fatalf("unexpected session list metadata: %#v", meta)
	}
	second := call(2, map[string]any{"cwd": cwd, "limit": 2, "cursor": cursor})
	secondResult := second["result"].(map[string]any)["result"].(map[string]any)
	if len(secondResult["sessions"].([]any)) != 1 || secondResult["nextCursor"] != nil {
		t.Fatalf("unexpected second session page: %#v", second)
	}
	filtered := call(3, map[string]any{"cwd": cwd, "_meta": map[string]any{"x.ai/facetFilters": map[string]any{"kind": []any{"chat"}}}})
	if rows := filtered["result"].(map[string]any)["result"].(map[string]any)["sessions"].([]any); len(rows) != 0 {
		t.Fatalf("kind filter retained build rows: %#v", filtered)
	}
}

func TestExtensionSessionCloseIsIdempotent(t *testing.T) {
	var output bytes.Buffer
	closed := 0
	server := &Server{output: &output, sessions: map[string]*session{"close-session": {
		id: "close-session", close: func() { closed++ },
	}}}
	call := func(id int) map[string]any {
		t.Helper()
		output.Reset()
		server.handleExtensionSessionClose(message{ID: json.RawMessage(strconv.Itoa(id)), Method: "x.ai/session/close", Params: json.RawMessage(`{"sessionId":"close-session"}`)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	for id := 1; id <= 2; id++ {
		response := call(id)
		extension := response["result"].(map[string]any)
		if extension["result"].(map[string]any)["success"] != true || extension["error"] != nil {
			t.Fatalf("unexpected extension close response: %#v", response)
		}
	}
	if closed != 1 {
		t.Fatalf("close function called %d times", closed)
	}
}

func TestMCPExtensionsWireContract(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	if err := registry.Register(fakeMCPTool{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeMCPResource{}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"mcp-session": {
		id: "mcp-session", cwd: root, runner: &agent.Runner{Tools: registry},
		mcpServers: []MCPServer{{Name: "fixture", Command: "fixture-server", Args: []string{"--stdio"}}},
	}}}
	call := func(id int, method string, params map[string]any) map[string]any {
		t.Helper()
		output.Reset()
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		server.handleMCP(context.Background(), message{ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: data})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	listed := call(1, "x.ai/mcp/list", map[string]any{"sessionId": "mcp-session"})
	servers := listed["result"].(map[string]any)["result"].(map[string]any)["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("unexpected MCP list response: %#v", listed)
	}
	serverEntry := servers[0].(map[string]any)
	toolEntries := serverEntry["session"].(map[string]any)["tools"].([]any)
	if serverEntry["name"] != "fixture" || serverEntry["type"] != "stdio" || len(toolEntries) != 1 || toolEntries[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("unexpected MCP server entry: %#v", serverEntry)
	}
	called := call(2, "x.ai/mcp/call", map[string]any{
		"sessionId": "mcp-session", "server": "fixture", "tool": "echo", "arguments": map[string]any{"value": "hello"},
	})
	content := called["result"].(map[string]any)["result"].(map[string]any)["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("unexpected MCP call response: %#v", called)
	}
	resource := call(3, "x.ai/mcp/read_resource", map[string]any{
		"sessionId": "mcp-session", "server": "fixture", "uri": "fixture://notes",
	})
	contents := resource["result"].(map[string]any)["result"].(map[string]any)["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["uri"] != "fixture://notes" || contents[0].(map[string]any)["text"] != "resource text" {
		t.Fatalf("unexpected MCP resource response: %#v", resource)
	}
}

func TestMCPAuthExtensionsForLocalServers(t *testing.T) {
	current := &session{id: "mcp-auth", runner: &agent.Runner{}, mcpServers: []MCPServer{{Name: "local", Command: "server"}}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"mcp-auth": current}}
	server.handleMCP(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/mcp/auth_status", Params: json.RawMessage(`{"session_id":"mcp-auth"}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)
	if result["error"] != nil || len(result["result"].(map[string]any)["servers"].([]any)) != 0 {
		t.Fatalf("unexpected MCP auth status: %#v", response)
	}
	output.Reset()
	server.handleMCP(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/mcp/auth_trigger", Params: json.RawMessage(`{"session_id":"mcp-auth","server_name":"local"}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result = response["result"].(map[string]any)
	trigger := result["result"].(map[string]any)
	if result["error"] != nil || trigger["status"] != "failed" || !strings.Contains(trigger["error"].(string), "not supported") {
		t.Fatalf("unexpected MCP auth trigger: %#v", response)
	}
}

func TestUpdateMCPServersExtension(t *testing.T) {
	var output bytes.Buffer
	var updated []mcppkg.ServerConfig
	runner := &agent.Runner{UpdateMCPServers: func(_ context.Context, servers []mcppkg.ServerConfig) error {
		updated = append([]mcppkg.ServerConfig(nil), servers...)
		return nil
	}}
	current := &session{id: "update-mcp", runner: runner}
	server := &Server{output: &output, sessions: map[string]*session{"update-mcp": current}}
	server.handleUpdateMCPServers(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/session/update_mcp_servers", Params: json.RawMessage(`{
		"sessionId":"update-mcp","mcpServers":[{"name":"local","command":"server","args":["--stdio"]}]
	}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	extension := response["result"].(map[string]any)
	if extension["result"].(map[string]any)["ok"] != true || extension["error"] != nil || len(updated) != 1 || updated[0].Name != "local" {
		t.Fatalf("unexpected MCP update response=%#v configs=%#v", response, updated)
	}
	current.mu.Lock()
	persisted := append([]MCPServer(nil), current.mcpServers...)
	current.mu.Unlock()
	if len(persisted) != 1 || persisted[0].Command != "server" {
		t.Fatalf("session MCP config was not updated: %#v", persisted)
	}
}

type fakeMCPTool struct{}

func (fakeMCPTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: mcppkg.ModelToolName("fixture", "echo")}
}

func (fakeMCPTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("unexpected model tool execution")
}

func (fakeMCPTool) MCPIdentity() (string, string, mcppkg.ToolInfo) {
	return "fixture", "echo", mcppkg.ToolInfo{Name: "echo", Title: "Echo", Description: "Echo one value"}
}

func (fakeMCPTool) CallMCP(_ context.Context, raw json.RawMessage) (mcppkg.ToolResult, error) {
	var arguments struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return mcppkg.ToolResult{}, err
	}
	var result mcppkg.ToolResult
	data, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": arguments.Value}}})
	return result, json.Unmarshal(data, &result)
}

type fakeMCPResource struct{}

func (fakeMCPResource) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: "mcp__resource__fixture__read"}
}

func (fakeMCPResource) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("unexpected model resource execution")
}

func (fakeMCPResource) MCPResourceReader() (string, bool) { return "fixture", true }

func (fakeMCPResource) ReadMCPResource(_ context.Context, uri string) ([]mcppkg.ResourceContents, error) {
	return []mcppkg.ResourceContents{{URI: uri, MIMEType: "text/plain", Text: "resource text"}}, nil
}

func TestStaticExtensionsAndCompactCommand(t *testing.T) {
	var output bytes.Buffer
	streamer := &fixtureStreamer{results: []api.StreamResult{{Text: "preserve the implementation state"}}}
	runner := &agent.Runner{Client: streamer, Model: "test-model"}
	current := &session{id: "compact-session", cwd: t.TempDir(), runner: runner, previous: "response-1", activePrompt: -1, close: func() {}}
	server := &Server{output: &output, sessions: map[string]*session{"compact-session": current}}
	server.handleStaticExtension(message{ID: json.RawMessage("1"), Method: "x.ai/commands/list", Params: json.RawMessage(`{}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	commands := response["result"].(map[string]any)["commands"].([]any)
	if len(commands) != 2 || commands[0].(map[string]any)["name"] != "compact" || commands[1].(map[string]any)["name"] != "loop" || commands[1].(map[string]any)["argumentHint"] != "[interval] <prompt>" {
		t.Fatalf("unexpected commands response: %#v", response)
	}
	output.Reset()
	server.handleStaticExtension(message{ID: json.RawMessage("2"), Method: "x.ai/workspaces/list", Params: json.RawMessage(`{}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	workspaceResult := response["result"].(map[string]any)["result"].(map[string]any)
	if len(workspaceResult["workspaces"].([]any)) != 0 || workspaceResult["_meta"].(map[string]any)["x.ai/partial"].(map[string]any)["reason"] != "no_oauth" {
		t.Fatalf("unexpected workspaces response: %#v", response)
	}
	output.Reset()
	params, _ := json.Marshal(map[string]any{"sessionId": "compact-session", "prompt": []any{map[string]any{"type": "text", "text": "/compact"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("3"), Method: "session/prompt", Params: params})
	server.wg.Wait()
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		if item["id"] == float64(3) {
			response = item
			break
		}
	}
	if result, ok := response["result"].(map[string]any); !ok || result["stopReason"] != "end_turn" {
		t.Fatalf("unexpected compact response: %#v", response)
	}
	current.mu.Lock()
	previous, running := current.previous, current.running
	current.mu.Unlock()
	if previous != "" || running {
		t.Fatalf("compact state previous=%q running=%v", previous, running)
	}
	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "response-1" || !strings.Contains(requests[0].Instructions, "handoff summary") {
		t.Fatalf("unexpected compact request: %#v", requests)
	}
}

func TestCompactConversationExtensionUsesContextWithoutPromptCompletion(t *testing.T) {
	var output bytes.Buffer
	streamer := &fixtureStreamer{results: []api.StreamResult{{Text: "focused summary"}}}
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "compact-extension")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	current := &session{
		id: "compact-extension", cwd: t.TempDir(), previous: "response-1", activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Logger: logger, Model: "test-model"},
	}
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleMemoryExtension(context.Background(), message{
		ID: json.RawMessage("4"), Method: "x.ai/compact_conversation",
		Params: json.RawMessage(`{"session_id":"compact-extension","user_context":"preserve the auth decision"}`),
	})
	server.wg.Wait()

	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[1]["id"] != float64(4) || len(messages[1]["result"].(map[string]any)) != 0 {
		t.Fatalf("messages=%#v", messages)
	}
	completed := messages[0]
	params := completed["params"].(map[string]any)
	update := params["update"].(map[string]any)
	meta := params["_meta"].(map[string]any)
	if completed["method"] != "x.ai/session_notification" || params["sessionId"] != current.id || update["sessionUpdate"] != "auto_compact_completed" || update["tokens_after"] != float64(0) || update["summary_preview"] != nil || meta["eventId"] == "" || meta["agentTimestampMs"].(float64) <= 0 {
		t.Fatalf("completion=%#v", completed)
	}
	for _, item := range messages {
		if item["method"] == "x.ai/session/prompt_complete" {
			t.Fatalf("compact extension emitted prompt completion: %#v", item)
		}
	}
	current.mu.Lock()
	previous, running := current.previous, current.running
	current.mu.Unlock()
	if previous != "" || running {
		t.Fatalf("previous=%q running=%v", previous, running)
	}
	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "response-1" || !strings.Contains(requests[0].Input[0].Content.(string), "preserve the auth decision") {
		t.Fatalf("requests=%#v", requests)
	}
	persisted, err := sessionlog.Events(logger.Path(), "xai_session_notification")
	if err != nil || len(persisted) != 1 {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}

	output.Reset()
	current.mu.Lock()
	current.previous, current.running = "response-2", true
	current.mu.Unlock()
	server.handleMemoryExtension(context.Background(), message{
		ID: json.RawMessage("5"), Method: "x.ai/compact_conversation",
		Params: json.RawMessage(`{"sessionId":"compact-extension","userContext":"keep tests"}`),
	})
	failed := decodeACPOutput(t, output.Bytes())
	if len(failed) != 1 || failed[0]["error"].(map[string]any)["message"] != "session already has an active prompt" {
		t.Fatalf("failed=%#v", failed)
	}

	output.Reset()
	current.mu.Lock()
	current.previous, current.running = "response-3", false
	current.runner.Client = promptErrorStreamer{err: errors.New("compact unavailable")}
	current.mu.Unlock()
	server.handleMemoryExtension(context.Background(), message{
		ID: json.RawMessage("6"), Method: "x.ai/compact_conversation",
		Params: json.RawMessage(`{"sessionId":"compact-extension"}`),
	})
	server.wg.Wait()
	modelFailure := decodeACPOutput(t, output.Bytes())
	if len(modelFailure) != 1 || modelFailure[0]["error"].(map[string]any)["message"] != "compact unavailable" {
		t.Fatalf("modelFailure=%#v", modelFailure)
	}
}

func TestTogglePlanModeNotificationPersistsAndPublishesMode(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "toggle-mode")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	current := &session{
		id: "toggle-mode", mode: "default",
		runner: &agent.Runner{Tools: registry, Logger: logger},
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	params := json.RawMessage(`{"sessionId":"toggle-mode"}`)
	server.handleTogglePlanMode(params)
	if currentMode(current) != "plan" || !registry.PlanModeActive() {
		t.Fatalf("mode=%q active=%v", currentMode(current), registry.PlanModeActive())
	}
	server.handleTogglePlanMode(params)
	if currentMode(current) != "default" || registry.PlanModeActive() {
		t.Fatalf("mode=%q active=%v", currentMode(current), registry.PlanModeActive())
	}
	if mode, err := sessionlog.CurrentMode(logger.Path()); err != nil || mode != "default" {
		t.Fatalf("persisted mode=%q err=%v", mode, err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	want := []string{"plan", "default"}
	for index, item := range messages {
		update := item["params"].(map[string]any)["update"].(map[string]any)
		if item["method"] != "session/update" || update["sessionUpdate"] != "current_mode_update" || update["currentModeId"] != want[index] {
			t.Fatalf("messages=%#v", messages)
		}
	}
	output.Reset()
	server.handleTogglePlanMode(json.RawMessage(`{"sessionId":"missing"}`))
	if output.Len() != 0 {
		t.Fatalf("unknown session produced output: %s", output.String())
	}
	current.runner.Logger = failingEventLogger{}
	server.handleTogglePlanMode(params)
	if currentMode(current) != "default" || registry.PlanModeActive() || output.Len() != 0 {
		t.Fatalf("failed persistence was not rolled back: mode=%q active=%v output=%q", currentMode(current), registry.PlanModeActive(), output.String())
	}
}

type failingEventLogger struct{}

func (failingEventLogger) Append(string, any) error {
	return errors.New("write failed")
}

func (failingEventLogger) AppendPrompt(string, []sessionlog.Content) error {
	return errors.New("write failed")
}

func (failingEventLogger) AppendSyntheticPrompt(string, []sessionlog.Content) error {
	return errors.New("write failed")
}

func TestXAINotificationWithoutPersistenceOmitsReplayCursor(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.notifyXAI(&session{id: "transient"}, map[string]any{"sessionUpdate": "auto_compact_completed"})
	messages := decodeACPOutput(t, output.Bytes())
	params := messages[0]["params"].(map[string]any)
	if _, exists := params["_meta"]; exists {
		t.Fatalf("unpersisted notification advertised a replay cursor: %#v", messages[0])
	}
}

func TestBtwExtensionRunsBesideMainTurnAndPreservesSessionState(t *testing.T) {
	sessionDir, cwd := t.TempDir(), t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "btw-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	ws, err := workspace.Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{Text: "The parser is being updated."}}}
	runner := &agent.Runner{
		Client: streamer, Tools: registry, Model: "test-model",
		SessionID: "btw-session", SessionPath: logger.Path(),
	}
	current := &session{
		id: "btw-session", runner: runner, previous: "response-1", running: true,
		activePrompt: -1, close: func() {},
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"btw-session": current}}
	server.handleBtw(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/btw",
		Params: json.RawMessage(`{"sessionId":"btw-session","question":"What is happening?"}`),
	})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	result := messages[0]["result"].(map[string]any)["result"].(map[string]any)
	if result["answer"] != "The parser is being updated." {
		t.Fatalf("messages=%#v", messages)
	}
	current.mu.Lock()
	previous, running, btwDone := current.previous, current.running, current.btwDone
	current.mu.Unlock()
	if previous != "response-1" || !running || btwDone != nil {
		t.Fatalf("previous=%q running=%v btwDone=%v", previous, running, btwDone)
	}
	streamer.mu.Lock()
	request := streamer.requests[0]
	streamer.mu.Unlock()
	if request.PreviousResponseID != "response-1" || len(request.Tools) == 0 || !strings.Contains(request.Input[0].Content.(string), "What is happening?") {
		t.Fatalf("request=%#v", request)
	}
	history, _ := sessionlog.BtwHistoryPath(logger.Path())
	data, err := os.ReadFile(history)
	if err != nil || !bytes.Contains(data, []byte(`"success":true`)) {
		t.Fatalf("history=%q err=%v", data, err)
	}
}

func TestBtwExtensionCloseCancelsAndWaitsForRequest(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "btw-close")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &blockingStreamer{started: make(chan struct{})}
	closed := make(chan struct{})
	current := &session{
		id: "btw-close", previous: "response-1", activePrompt: -1,
		runner: &agent.Runner{Client: streamer, SessionID: "btw-close", SessionPath: logger.Path()},
		close:  func() { close(closed) },
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"btw-close": current}}
	server.handleBtw(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/btw",
		Params: json.RawMessage(`{"sessionId":"btw-close","question":"status?"}`),
	})
	select {
	case <-streamer.started:
	case <-time.After(time.Second):
		t.Fatal("side question did not start")
	}
	server.handleBtw(context.Background(), message{
		ID: json.RawMessage("2"), Method: "x.ai/btw",
		Params: json.RawMessage(`{"sessionId":"btw-close","question":"again?"}`),
	})
	if !server.closeSession("btw-close") {
		t.Fatal("session was not closed")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("session close did not wait for side question cancellation")
	}
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["error"] == nil || messages[1]["error"] == nil {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestBtwExtensionRejectsInvalidRequests(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "btw-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{Text: "unused"}}}
	current := &session{
		id: "btw-session", activePrompt: -1, close: func() {},
		runner: &agent.Runner{Client: streamer, SessionID: "btw-session", SessionPath: logger.Path()},
	}
	for _, params := range []string{
		`{"sessionId":"btw-session"}`,
		`{"sessionId":"missing","question":"status?"}`,
		`{"sessionId":"btw-session","question":"   "}`,
	} {
		var output bytes.Buffer
		server := &Server{output: &output, sessions: map[string]*session{"btw-session": current}}
		server.handleBtw(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/btw", Params: json.RawMessage(params)})
		server.wg.Wait()
		messages := decodeACPOutput(t, output.Bytes())
		if len(messages) != 1 || messages[0]["error"] == nil {
			t.Fatalf("params=%s messages=%#v", params, messages)
		}
	}
	current.mu.Lock()
	current.closed = true
	current.mu.Unlock()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"btw-session": current}}
	server.handleBtw(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/btw",
		Params: json.RawMessage(`{"sessionId":"btw-session","question":"status?"}`),
	})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["error"] == nil {
		t.Fatalf("closed messages=%#v", messages)
	}
	streamer.mu.Lock()
	requests := len(streamer.requests)
	streamer.mu.Unlock()
	current.mu.Lock()
	btwDone := current.btwDone
	current.mu.Unlock()
	if requests != 0 || btwDone != nil {
		t.Fatalf("requests=%d btwDone=%v", requests, btwDone)
	}
}

func TestMemoryFlushExtensionAndSlashCommand(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, cwd, "memory-session")
	if err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	ws, err := workspace.Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := tools.RegisterMemoryTools(registry, store, config); err != nil {
		t.Fatal(err)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "flush-one", Text: "## Decision\n\nPersist useful context."},
		{ResponseID: "flush-two", Text: "NO_REPLY"},
		{ResponseID: "rewrite-one", Text: "## Deployment\n\n- Run release checks."},
	}}
	runner := &agent.Runner{Client: streamer, Model: "test", Tools: registry, Memory: store, MemoryConfig: config, OpenMemory: func() (*memory.Store, error) { return memory.Open(root, cwd, "memory-session") }}
	current := &session{id: "memory-session", cwd: cwd, runner: runner, previous: "response-1", activePrompt: -1, close: func() {}}
	var output bytes.Buffer
	server := &Server{output: &output, MemoryEnabled: true, sessions: map[string]*session{"memory-session": current}}
	server.handleStaticExtension(message{ID: json.RawMessage("0"), Method: "x.ai/commands/list", Params: json.RawMessage(`{}`)})
	var commandResponse map[string]any
	if err := json.NewDecoder(&output).Decode(&commandResponse); err != nil {
		t.Fatal(err)
	}
	commands := commandResponse["result"].(map[string]any)["commands"].([]any)
	if len(commands) != 5 || commands[1].(map[string]any)["name"] != "flush" || commands[2].(map[string]any)["name"] != "dream" || commands[3].(map[string]any)["name"] != "memory" {
		t.Fatalf("memory commands=%#v", commands)
	}
	output.Reset()
	server.MemoryEnabled = false
	server.handleStaticExtension(message{ID: json.RawMessage("0"), Method: "x.ai/commands/list", Params: json.RawMessage(`{}`)})
	if err := json.NewDecoder(&output).Decode(&commandResponse); err != nil {
		t.Fatal(err)
	}
	commands = commandResponse["result"].(map[string]any)["commands"].([]any)
	if len(commands) != 2 {
		t.Fatalf("disabled memory commands=%#v", commands)
	}
	server.MemoryEnabled = true
	output.Reset()
	server.handleMemoryExtension(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/memory/flush", Params: json.RawMessage(`{"session_id":"memory-session"}`),
	})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	started, completed, responded := false, false, false
	for _, message := range messages {
		if message["method"] == "x.ai/session/prompt_complete" {
			t.Fatalf("memory extension emitted prompt completion: %#v", message)
		}
		if message["id"] != nil {
			responded = message["error"] == nil
			continue
		}
		params, _ := message["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		started = started || update["sessionUpdate"] == "memory_flush_started"
		completed = completed || update["sessionUpdate"] == "memory_flush_completed" && update["result"] == "written"
	}
	if !started || !completed || !responded {
		t.Fatalf("messages=%#v", messages)
	}
	if value, err := store.Context(); err != nil || !strings.Contains(value, "Persist useful context") {
		t.Fatalf("memory=%q err=%v", value, err)
	}
	current.mu.Lock()
	previous := current.previous
	current.mu.Unlock()
	if previous != "response-1" {
		t.Fatalf("flush changed response chain to %q", previous)
	}

	output.Reset()
	params, _ := json.Marshal(map[string]any{"sessionId": "memory-session", "prompt": []any{map[string]any{"type": "text", "text": "/flush"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("2"), Method: "session/prompt", Params: params})
	server.wg.Wait()
	messages = decodeACPOutput(t, output.Bytes())
	responded = false
	for _, message := range messages {
		if result, ok := message["result"].(map[string]any); ok && result["stopReason"] == "end_turn" {
			responded = true
		}
	}
	if !responded {
		t.Fatalf("slash messages=%#v", messages)
	}

	output.Reset()
	params, _ = json.Marshal(map[string]any{"sessionId": "memory-session", "prompt": []any{map[string]any{"type": "text", "text": "/memory"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("3"), Method: "session/prompt", Params: params})
	messages = decodeACPOutput(t, output.Bytes())
	listed, responded := false, false
	for _, message := range messages {
		if result, ok := message["result"].(map[string]any); ok && result["stopReason"] == "end_turn" {
			responded = true
		}
		params, _ := message["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		files, _ := update["files"].([]any)
		if update["sessionUpdate"] == "memory_files" && len(files) == 1 {
			file := files[0].(map[string]any)
			listed = file["source"] == "session" && file["size_bytes"].(float64) > 0 && file["modified_epoch_secs"].(float64) > 0
		}
	}
	if !listed || !responded {
		t.Fatalf("memory list messages=%#v", messages)
	}

	for _, toggle := range []struct {
		prompt, text string
		enabled      bool
	}{{"/mem off", "Memory disabled for this session.", false}, {"/memory enable", "Memory enabled for this session.", true}} {
		output.Reset()
		params, _ = json.Marshal(map[string]any{"sessionId": "memory-session", "prompt": []any{map[string]any{"type": "text", "text": toggle.prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(`"toggle"`), Method: "session/prompt", Params: params})
		server.wg.Wait()
		messages = decodeACPOutput(t, output.Bytes())
		textSeen, responseSeen := false, false
		for _, message := range messages {
			if result, ok := message["result"].(map[string]any); ok && result["stopReason"] == "end_turn" {
				responseSeen = true
			}
			params, _ := message["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			textSeen = textSeen || content["text"] == toggle.text
		}
		if !textSeen || !responseSeen || runner.MemoryConfig.Enabled != toggle.enabled || registry.HasTool("memory_search") != toggle.enabled || registry.HasTool("memory_get") != toggle.enabled {
			t.Fatalf("toggle=%#v messages=%#v enabled=%v", toggle, messages, runner.MemoryConfig.Enabled)
		}
	}

	output.Reset()
	server.handleMemoryExtension(context.Background(), message{
		ID: json.RawMessage("4"), Method: "x.ai/memory/rewrite",
		Params: json.RawMessage(`{"sessionId":"memory-session","rawText":"run release checks","contextSummary":"deployment workflow"}`),
	})
	server.wg.Wait()
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["rewritten"] != "## Deployment\n\n- Run release checks." {
		t.Fatalf("rewrite messages=%#v", messages)
	}
	streamer.mu.Lock()
	rewriteRequest := streamer.requests[len(streamer.requests)-1]
	streamer.mu.Unlock()
	if rewriteRequest.Model != "grok-build" || rewriteRequest.PreviousResponseID != "" || rewriteRequest.Temperature == nil || *rewriteRequest.Temperature != 0.3 {
		t.Fatalf("rewrite request=%#v", rewriteRequest)
	}
	current.mu.Lock()
	previous = current.previous
	current.mu.Unlock()
	if previous != "response-1" {
		t.Fatalf("rewrite changed response chain to %q", previous)
	}

	output.Reset()
	oversize, _ := json.Marshal(map[string]any{"sessionId": "memory-session", "rawText": strings.Repeat("x", (32<<10)+1), "contextSummary": ""})
	server.handleMemoryExtension(context.Background(), message{ID: json.RawMessage("5"), Method: "x.ai/memory/rewrite", Params: oversize})
	server.wg.Wait()
	messages = decodeACPOutput(t, output.Bytes())
	current.mu.Lock()
	running, runDone, cancel := current.running, current.runDone, current.cancel
	current.mu.Unlock()
	if len(messages) != 1 || messages[0]["error"] == nil || running || runDone != nil || cancel != nil {
		t.Fatalf("oversize messages=%#v running=%v runDone=%v cancel=%v", messages, running, runDone, cancel)
	}
}

func TestMemoryDreamSlashCommandConsolidatesAndNotifies(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	prior, err := memory.Open(root, cwd, "prior")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := prior.Write("session_end", "## Decision\n\nKeep this knowledge.")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(root, cwd, "dream-session")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	streamer := &fixtureStreamer{results: []api.StreamResult{{Text: "## Architecture\n\nKeep clear boundaries."}}}
	runner := &agent.Runner{Client: streamer, Model: "test", Memory: store, MemoryConfig: cfg}
	current := &session{id: "dream-session", cwd: cwd, runner: runner, activePrompt: -1, close: func() {}}
	var output bytes.Buffer
	server := &Server{output: &output, MemoryEnabled: true, sessions: map[string]*session{"dream-session": current}}
	params, _ := json.Marshal(map[string]any{"sessionId": "dream-session", "prompt": []any{map[string]any{"type": "text", "text": "/dream"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("1"), Method: "session/prompt", Params: params})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	notified, responded := false, false
	for _, item := range messages {
		if result, ok := item["result"].(map[string]any); ok && result["stopReason"] == "end_turn" {
			responded = true
		}
		params, _ := item["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		notified = notified || update["sessionUpdate"] == "memory_dream_completed" && update["result"] == "written" && update["path"] != ""
	}
	if !notified || !responded {
		t.Fatalf("messages=%#v", messages)
	}
}

func decodeACPOutput(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var messages []map[string]any
	for {
		var message map[string]any
		if err := decoder.Decode(&message); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	return messages
}

func TestLoopCommandExpandsBeforeModelTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "loop-response", Text: "scheduled"}}}
	current := &session{
		id: "loop-session", ctx: context.Background(), cwd: t.TempDir(), activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test-model"},
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"loop-session": current}}
	server.handlePrompt(context.Background(), message{
		ID: json.RawMessage("1"), Method: "session/prompt",
		Params: json.RawMessage(`{"sessionId":"loop-session","prompt":[{"type":"text","text":"/loop every hour check deploy"}]}`),
	})
	server.wg.Wait()
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 1 {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[0].Input)
	if !strings.Contains(string(input), "scheduler_create") || !strings.Contains(string(input), "every hour check deploy") || strings.Contains(string(input), `\"/loop`) {
		t.Fatalf("loop input=%s", input)
	}
}

func TestSkillsExtensionWireContract(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".grok", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review changes\n---\nReview the diff.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	current := &session{id: "skill-session", cwd: root, runner: &agent.Runner{Skills: catalog}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"skill-session": current}}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/skills/list", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	items := response["result"].(map[string]any)["result"].(map[string]any)["skills"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "review" || items[0].(map[string]any)["scope"] != "local" || items[0].(map[string]any)["enabled"] != true {
		t.Fatalf("unexpected skills list: %#v", response)
	}
	output.Reset()
	server.handleSkills(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/skills/config", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	config := response["result"].(map[string]any)["result"].(map[string]any)
	if config["totalSkills"].(float64) != 1 || len(config["skills"].([]any)) != 1 {
		t.Fatalf("unexpected skills config: %#v", response)
	}
}

func TestSkillsMutationExtensions(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "custom")
	skillDir := filepath.Join(custom, "review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review changes\n---\nReview.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := skills.Settings{}
	runner := &agent.Runner{Skills: catalog}
	runner.UpdateSkills = func(_ context.Context, update func(*skills.Settings)) (skills.Settings, error) {
		update(&settings)
		err := catalog.Reconfigure(settings)
		return settings, err
	}
	current := &session{id: "skills-mutate", cwd: root, runner: runner}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"skills-mutate": current}}

	decode := func() map[string]any {
		t.Helper()
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		return response["result"].(map[string]any)["result"].(map[string]any)
	}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/skills/add", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `,"path":` + strconv.Quote(custom) + `}`)})
	added := decode()
	if added["addedCount"].(float64) != 1 || added["total"].(float64) != 1 || len(settings.Paths) != 1 {
		t.Fatalf("unexpected add result=%#v settings=%#v", added, settings)
	}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/skills/toggle", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `,"name":"review","enabled":false}`)})
	toggled := decode()
	if toggled["skills"].([]any)[0].(map[string]any)["enabled"] != false || strings.Join(settings.Disabled, "|") != "review" {
		t.Fatalf("unexpected toggle result=%#v settings=%#v", toggled, settings)
	}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/skills/remove", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `,"path":` + strconv.Quote(custom) + `}`)})
	removed := decode()
	if len(removed["skills"].([]any)) != 0 || len(settings.Paths) != 0 {
		t.Fatalf("unexpected remove result=%#v settings=%#v", removed, settings)
	}
	settings.Paths = []string{custom}
	settings.Disabled = []string{"review"}
	if err := catalog.Reconfigure(settings); err != nil {
		t.Fatal(err)
	}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/skills/reset", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `}`)})
	reset := decode()
	if len(settings.Paths) != 0 || len(settings.Disabled) != 0 || len(reset["skills"].([]any)) != 0 {
		t.Fatalf("unexpected reset result=%#v settings=%#v", reset, settings)
	}
}

func TestPluginsListExtensionWireContract(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(skillRoot, "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(root, ".mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"docs":{"command":"docs"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agentRoot := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentRoot, "review.md"), []byte("---\nname: reviewer\ndescription: Review code\n---\nReview."), 0o600); err != nil {
		t.Fatal(err)
	}
	hooksRoot := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksRoot, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"check"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plugins := []plugin.Plugin{{
		ID: "project/12345678/review-tools", Name: "review-tools", Scope: "project", Root: root,
		Version: "1.0.0", SkillDirs: []string{skillRoot}, AgentDirs: []string{agentRoot}, HooksConfig: hooksPath,
		MCPConfig: mcpPath, Enabled: false, Trusted: false,
	}}
	current := &session{id: "plugin-session", runner: &agent.Runner{PluginInventory: func() []plugin.Plugin { return plugins }}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"plugin-session": current}}
	server.handlePlugins(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/plugins/list", Params: json.RawMessage(`{"sessionId":"plugin-session"}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)
	items := result["result"].(map[string]any)["plugins"].([]any)
	item := items[0].(map[string]any)
	if result["error"] != nil || len(items) != 1 || item["enabled"] != false || item["trusted"] != false || item["scope"] != "project" || item["skillCount"].(float64) != 1 || item["agentCount"].(float64) != 1 || item["agentNames"].([]any)[0] != "review" || item["hookCount"].(float64) != 1 || item["hookStatus"] != "blocked" || item["mcpServerCount"].(float64) != 1 || item["mcpStatus"] != "blocked" {
		t.Fatalf("unexpected plugins list: %#v", response)
	}
}

func TestHooksListAndDisableWireContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	hooksRoot := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksRoot, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"PreToolUse":[{"matcher":"shell","hooks":[{"type":"command","command":"check","timeout":2}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := hooks.Discover(hooks.Config{ProjectTrusted: true, Plugins: []plugin.Plugin{{Name: "guard", Root: root, HooksConfig: hooksPath, Executable: true}}})
	reloads := 0
	current := &session{id: "hook-session", cwd: root, runner: &agent.Runner{
		HookCatalog: catalog, ReloadHooks: func() error { reloads++; return nil },
	}}
	request := func(method, params string) map[string]any {
		var output bytes.Buffer
		server := &Server{output: &output, sessions: map[string]*session{"hook-session": current}}
		server.handleHooks(context.Background(), message{ID: json.RawMessage("1"), Method: method, Params: json.RawMessage(params)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response["result"].(map[string]any)["result"].(map[string]any)
	}
	listed := request("x.ai/hooks/list", `{"sessionId":"hook-session"}`)
	items := listed["hooks"].([]any)
	item := items[0].(map[string]any)
	if len(items) != 1 || listed["projectTrusted"] != true || item["event"] != "pre_tool_use" || item["matcher"] != "shell" || item["timeoutMs"].(float64) != 2000 || item["disabled"] != false {
		t.Fatalf("listed=%#v", listed)
	}
	name := item["name"].(string)
	outcome := request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"disable","hookName":`+strconv.Quote(name)+`}}`)
	if outcome["status"] != "success" || !catalog.Snapshot().Hooks[0].Disabled {
		t.Fatalf("outcome=%#v snapshot=%#v", outcome, catalog.Snapshot())
	}
	request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"reload"}}`)
	if reloads != 1 {
		t.Fatalf("reloads=%d", reloads)
	}
	custom := filepath.Join(home, "custom", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	added := request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"add","path":`+strconv.Quote(custom)+`}}`)
	if added["status"] != "success" || reloads != 2 {
		t.Fatalf("added=%#v reloads=%d", added, reloads)
	}
	removed := request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"remove","path":`+strconv.Quote(custom)+`}}`)
	if removed["status"] != "success" || reloads != 3 {
		t.Fatalf("removed=%#v reloads=%d", removed, reloads)
	}
	componentReloads := 0
	current.runner.UpdatePlugins = func(context.Context, func(*plugin.Settings)) ([]plugin.Plugin, error) {
		componentReloads++
		return nil, nil
	}
	trusted := request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"trust"}}`)
	untrusted := request("x.ai/hooks/action", `{"sessionId":"hook-session","action":{"type":"untrust"}}`)
	if trusted["status"] != "success" || untrusted["status"] != "success" || trusted["requiresRestart"] != false || untrusted["requiresRestart"] != false || componentReloads != 2 {
		t.Fatalf("trusted=%#v untrusted=%#v reloads=%d", trusted, untrusted, componentReloads)
	}
}

func TestPluginWireInfoReportsInlineHooks(t *testing.T) {
	info := pluginWireInfo(plugin.Plugin{
		Name: "inline", Enabled: true, Trusted: true, Executable: true,
		InlineHooks: json.RawMessage(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"start"}]}]}}`),
	})
	if info["hookStatus"] != "active_inline" || info["hookCount"] != 1 {
		t.Fatalf("info=%#v", info)
	}
}

func TestTaskListAndKillWireContract(t *testing.T) {
	killed := ""
	current := &session{id: "task-session", runner: &agent.Runner{
		ListTasks: func() []tools.ProcessSnapshot {
			return []tools.ProcessSnapshot{{TaskID: "task-1", Command: "sleep 1", CWD: "/work", StartTime: tools.ProcessTime{SecsSinceEpoch: 10}, Kind: "bash"}}
		},
		KillTask: func(_ context.Context, id string) (string, error) { killed = id; return "killed", nil },
	}}
	request := func(method, params string) map[string]any {
		var output bytes.Buffer
		server := &Server{output: &output, sessions: map[string]*session{"task-session": current}}
		server.handleTasks(context.Background(), message{ID: json.RawMessage("1"), Method: method, Params: json.RawMessage(params)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response["result"].(map[string]any)
	}
	listed := request("x.ai/task/list", `{"sessionId":"task-session"}`)
	item := listed["result"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
	if item["task_id"] != "task-1" || item["command"] != "sleep 1" || item["cwd"] != "/work" || item["kind"] != "bash" {
		t.Fatalf("listed=%#v", listed)
	}
	kill := request("x.ai/task/kill", `{"sessionId":"task-session","taskId":"task-1"}`)
	if killed != "task-1" || kill["result"].(map[string]any)["outcome"] != "killed" {
		t.Fatalf("kill=%#v killed=%q", kill, killed)
	}
}

func TestTaskLifecycleNotificationsWireContract(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.NotifyTaskBackgrounded("session-1", tools.ProcessBackgrounded{
		ToolCallID: "call-1", TaskID: "task-1", Command: "sleep 1", CWD: "/work", Description: "wait",
	})
	code := 0
	server.NotifyTaskCompleted("session-1", tools.ProcessSnapshot{
		TaskID: "task-1", Command: "sleep 1", CWD: "/work", StartTime: tools.ProcessTime{SecsSinceEpoch: 10},
		Completed: true, ExitCode: &code, Kind: "bash",
	}, false)
	decoder := json.NewDecoder(&output)
	started := decodeACP(t, decoder)
	if started["method"] != "x.ai/task_backgrounded" {
		t.Fatalf("started=%#v", started)
	}
	startedParams := started["params"].(map[string]any)
	startedUpdate := startedParams["update"].(map[string]any)
	if startedParams["sessionId"] != "session-1" || startedUpdate["sessionUpdate"] != "task_backgrounded" || startedUpdate["tool_call_id"] != "call-1" || startedUpdate["task_id"] != "task-1" || startedUpdate["description"] != "wait" {
		t.Fatalf("started=%#v", started)
	}
	completed := decodeACP(t, decoder)
	if completed["method"] != "x.ai/task_completed" {
		t.Fatalf("completed=%#v", completed)
	}
	completedUpdate := completed["params"].(map[string]any)["update"].(map[string]any)
	snapshot := completedUpdate["task_snapshot"].(map[string]any)
	if completedUpdate["sessionUpdate"] != "task_completed" || completedUpdate["will_wake"] != false || snapshot["task_id"] != "task-1" || snapshot["completed"] != true {
		t.Fatalf("completed=%#v", completed)
	}
}

func TestSubagentLifecycleNotificationsWireContract(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.NotifySubagentStarted("parent-1", subagent.Started{
		ID: "child-1", Type: "explore", Description: "find code", Model: "grok-4",
		CapabilityMode: "read-only", ResumedFrom: "child-0",
	})
	server.NotifySubagentProgress("parent-1", tools.SubagentResult{
		ID: "child-1", DurationMS: 2500, Turns: 2, ToolCalls: 3, TokensUsed: 1200,
		ContextWindow: 256000, ContextUsage: 40, ToolsUsed: []string{"read_file", "grep"}, ErrorCount: 1,
	})
	server.NotifySubagentEnded("parent-1", tools.SubagentResult{
		ID: "child-1", Status: "completed", Output: "done", DurationMS: 3000,
		Turns: 2, ToolCalls: 3, TokensUsed: 1400,
	})

	decoder := json.NewDecoder(&output)
	spawned := decodeACP(t, decoder)
	spawnedParams := spawned["params"].(map[string]any)
	spawnedUpdate := spawnedParams["update"].(map[string]any)
	if spawned["method"] != "x.ai/session_notification" || spawnedParams["sessionId"] != "parent-1" || spawnedUpdate["sessionUpdate"] != "subagent_spawned" || spawnedUpdate["subagent_id"] != "child-1" || spawnedUpdate["parent_session_id"] != "parent-1" || spawnedUpdate["child_session_id"] != "child-1" || spawnedUpdate["subagent_type"] != "explore" || spawnedUpdate["effective_context_source"] != "resumed" || spawnedUpdate["context_normalized"] != false || spawnedUpdate["capability_mode"] != "read-only" || spawnedUpdate["model"] != "grok-4" || spawnedUpdate["resumed_from"] != "child-0" {
		t.Fatalf("spawned=%#v", spawned)
	}
	progress := decodeACP(t, decoder)
	progressUpdate := progress["params"].(map[string]any)["update"].(map[string]any)
	if progress["method"] != "x.ai/session_notification" || progressUpdate["sessionUpdate"] != "subagent_progress" || progressUpdate["duration_ms"] != float64(2500) || progressUpdate["turn_count"] != float64(2) || progressUpdate["tool_call_count"] != float64(3) || progressUpdate["tokens_used"] != float64(1200) || progressUpdate["context_window_tokens"] != float64(256000) || progressUpdate["context_usage_pct"] != float64(40) || len(progressUpdate["tools_used"].([]any)) != 2 || progressUpdate["error_count"] != float64(1) {
		t.Fatalf("progress=%#v", progress)
	}
	finished := decodeACP(t, decoder)
	finishedUpdate := finished["params"].(map[string]any)["update"].(map[string]any)
	if finished["method"] != "x.ai/session_notification" || finishedUpdate["sessionUpdate"] != "subagent_finished" || finishedUpdate["status"] != "completed" || finishedUpdate["output"] != "done" || finishedUpdate["tool_calls"] != float64(3) || finishedUpdate["turns"] != float64(2) || finishedUpdate["duration_ms"] != float64(3000) || finishedUpdate["tokens_used"] != float64(1400) || finishedUpdate["will_wake"] != false || finishedUpdate["error"] != nil {
		t.Fatalf("finished=%#v", finished)
	}
}

func TestSessionReplayIncludesSubagentLifecycle(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "parent-1")
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": t.TempDir()}},
		{"user_prompt", map[string]any{"text": "delegate"}},
		{"model_response", map[string]any{"text": "working", "response_id": "r1", "tool_call_count": 0}},
		{"subagent_spawned", SubagentStartedUpdate("parent-1", subagent.Started{ID: "child-1", Type: "explore", Description: "find"})},
		{"subagent_finished", SubagentFinishedUpdate(tools.SubagentResult{ID: "child-1", Type: "explore", Status: "completed", Output: "done"})},
		{"task_backgrounded", TaskBackgroundedUpdate(tools.ProcessBackgrounded{TaskID: "task-1", Command: "make test", CWD: "/work"})},
		{"task_completed", TaskCompletedUpdate(tools.ProcessSnapshot{TaskID: "task-1", Command: "make test", Completed: true, ExitCode: &code}, true)},
		{"xai_session_notification", map[string]any{
			"sessionId": "parent-1", "update": map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_after": 0},
			"_meta": map[string]any{"eventId": "parent-1-9", "agentTimestampMs": 10},
		}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output}
	if err := server.replaySession(path, "parent-1"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var messages []map[string]any
	for {
		var message map[string]any
		if err := decoder.Decode(&message); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if len(messages) != 7 {
		t.Fatalf("messages=%#v", messages)
	}
	spawn := messages[2]["params"].(map[string]any)["update"].(map[string]any)
	finish := messages[3]["params"].(map[string]any)["update"].(map[string]any)
	if messages[2]["method"] != "x.ai/session_notification" || spawn["sessionUpdate"] != "subagent_spawned" || spawn["subagent_id"] != "child-1" || finish["sessionUpdate"] != "subagent_finished" || finish["output"] != "done" {
		t.Fatalf("messages=%#v", messages)
	}
	taskStarted := messages[4]["params"].(map[string]any)["update"].(map[string]any)
	taskFinished := messages[5]["params"].(map[string]any)["update"].(map[string]any)
	if messages[4]["method"] != "x.ai/task_backgrounded" || taskStarted["task_id"] != "task-1" || messages[5]["method"] != "x.ai/task_completed" || taskFinished["will_wake"] != true {
		t.Fatalf("task lifecycle=%#v", messages[4:])
	}
	compactParams := messages[6]["params"].(map[string]any)
	if messages[6]["method"] != "x.ai/session_notification" || compactParams["update"].(map[string]any)["sessionUpdate"] != "auto_compact_completed" || compactParams["_meta"].(map[string]any)["eventId"] != "parent-1-9" {
		t.Fatalf("compact replay=%#v", messages[6])
	}
}

func TestSyntheticWakeQueueRunsSerially(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "parent-1")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Append("session_metadata", map[string]any{"cwd": root}); err != nil {
		t.Fatal(err)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "wake-1", Text: "first wake"},
		{ResponseID: "wake-2", Text: "second wake"},
		{ResponseID: "wake-3", Text: "task wake"},
	}}
	current := &session{
		id: "parent-1", ctx: context.Background(), cwd: root, previous: "parent-response", activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Logger: logger, Model: "test"}, logPath: logger.Path(),
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"parent-1": current}}
	first := tools.SubagentResult{ID: "child-1", Type: "explore", Description: "first", Status: "completed", DurationMS: 1000, WillWake: true}
	second := tools.SubagentResult{ID: "child-2", Type: "general-purpose", Description: "second", Status: "failed", DurationMS: 2000, WillWake: true}
	code := 0
	task := tools.ProcessSnapshot{TaskID: "task-1", Command: "make test", Completed: true, ExitCode: &code}
	if !server.QueueSubagentWake("parent-1", first) || !server.QueueSubagentWake("parent-1", second) || !server.QueueTaskWake("parent-1", task) {
		t.Fatal("wake queue rejected an active session")
	}
	server.NotifySubagentEnded("parent-1", first)
	server.NotifySubagentEnded("parent-1", second)
	server.NotifyTaskCompleted("parent-1", task, true)
	server.wg.Wait()
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 3 || streamer.requests[0].PreviousResponseID != "parent-response" || streamer.requests[1].PreviousResponseID != "wake-1" || streamer.requests[2].PreviousResponseID != "wake-2" {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	firstInput, _ := json.Marshal(streamer.requests[0].Input)
	secondInput, _ := json.Marshal(streamer.requests[1].Input)
	taskInput, _ := json.Marshal(streamer.requests[2].Input)
	if !strings.Contains(string(firstInput), "child-1") || !strings.Contains(string(secondInput), "child-2") || !strings.Contains(string(taskInput), "task-1") {
		t.Fatalf("inputs=%s / %s / %s", firstInput, secondInput, taskInput)
	}
	transcript, err := sessionlog.Transcript(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 3 || transcript[0].Text != "first wake" || transcript[1].Text != "second wake" || transcript[2].Text != "task wake" {
		t.Fatalf("transcript=%#v", transcript)
	}
	for _, message := range transcript {
		if message.Role == "user" || strings.Contains(message.Text, "child-1") || strings.Contains(message.Text, "child-2") {
			t.Fatalf("synthetic prompt leaked into transcript: %#v", transcript)
		}
	}
	history, err := sessionlog.PromptHistory(dir, root, "parent-1", true)
	if err != nil || len(history) != 0 {
		t.Fatalf("synthetic prompt leaked into history: %#v err=%v", history, err)
	}
	current.mu.Lock()
	if current.running || len(current.wakeQueue) != 0 || current.previous != "wake-3" {
		current.mu.Unlock()
		t.Fatalf("session=%#v", current)
	}
	current.running = true
	current.mu.Unlock()
	third := tools.SubagentResult{ID: "child-3", Type: "explore", Status: "completed"}
	if !server.QueueSubagentWake("parent-1", third) {
		t.Fatal("queued wake was rejected")
	}
	server.CancelSubagentWake("parent-1", third.ID)
	current.mu.Lock()
	if len(current.wakeQueue) != 0 {
		current.mu.Unlock()
		t.Fatalf("consumed wake remained queued: %#v", current.wakeQueue)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	current.ctx, current.running = cancelled, false
	current.mu.Unlock()
	if server.QueueSubagentWake("parent-1", tools.SubagentResult{ID: "child-4"}) {
		t.Fatal("wake queue accepted a cancelled parent context")
	}
}

func TestMonitorEventsUseWireChannelAndSyntheticQueue(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "parent-monitor")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "monitor-wake", Text: "observed"}, {ResponseID: "completion-wake", Text: "done"}}}
	current := &session{
		id: "parent-monitor", ctx: context.Background(), cwd: root, previous: "parent-response", running: true, activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Logger: logger, Model: "test"}, logPath: logger.Path(),
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"parent-monitor": current}}
	server.NotifyMonitorEvent("parent-monitor", tools.MonitorEvent{TaskID: "monitor-1", Description: "watch build", EventText: "step 1"})
	server.NotifyMonitorEvent("parent-monitor", tools.MonitorEvent{TaskID: "monitor-2", Description: "watch tests", EventText: "case 2"})
	decoder := json.NewDecoder(&output)
	for _, taskID := range []string{"monitor-1", "monitor-2"} {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		params := message["params"].(map[string]any)
		update := params["update"].(map[string]any)
		if message["method"] != "x.ai/monitor_event" || params["sessionId"] != "parent-monitor" || update["task_id"] != taskID || update["sessionUpdate"] != "monitor_event" {
			t.Fatalf("monitor wire message=%#v", message)
		}
	}
	current.mu.Lock()
	current.running = false
	current.mu.Unlock()
	server.startNextWake(current)
	server.wg.Wait()
	streamer.mu.Lock()
	if len(streamer.requests) != 1 {
		streamer.mu.Unlock()
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[0].Input)
	streamer.mu.Unlock()
	if !strings.Contains(string(input), "monitor-1") || !strings.Contains(string(input), "monitor-2") || !strings.Contains(string(input), "step 1") || !strings.Contains(string(input), "case 2") {
		t.Fatalf("batched monitor input=%s", input)
	}

	current.mu.Lock()
	current.running = true
	current.mu.Unlock()
	server.NotifyMonitorEvent("parent-monitor", tools.MonitorEvent{TaskID: "monitor-done", Description: "watch done", EventText: "last line"})
	code := 0
	snapshot := tools.ProcessSnapshot{TaskID: "monitor-done", Command: "watch", Kind: "monitor", Completed: true, ExitCode: &code}
	if !server.QueueTaskWake("parent-monitor", snapshot) {
		t.Fatal("completion wake rejected")
	}
	current.mu.Lock()
	current.running = false
	current.mu.Unlock()
	server.NotifyTaskCompleted("parent-monitor", snapshot, true)
	server.wg.Wait()
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 2 {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	completionInput, _ := json.Marshal(streamer.requests[1].Input)
	if strings.Contains(string(completionInput), "last line") || !strings.Contains(string(completionInput), "monitor-done") || streamer.requests[1].PreviousResponseID != "monitor-wake" {
		t.Fatalf("completion input=%s request=%#v", completionInput, streamer.requests[1])
	}
}

func TestScheduledTaskNotificationsQueueOnceAndDelete(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "parent-scheduler")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "scheduled-response", Text: "checked"}}}
	current := &session{
		id: "parent-scheduler", ctx: context.Background(), cwd: root, previous: "parent-response", running: true, activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Logger: logger, Model: "test"}, logPath: logger.Path(),
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"parent-scheduler": current}}
	next := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	event := tools.ScheduledTaskFired{TaskID: "loop-1", Prompt: "check deployment", HumanSchedule: "every 1 minute", NextFireAt: &next}
	server.NotifyScheduledTaskFired("parent-scheduler", event)
	server.NotifyScheduledTaskFired("parent-scheduler", event)
	decoder := json.NewDecoder(&output)
	for index, method := range []string{"x.ai/scheduled_task_inject_prompt", "x.ai/scheduled_task_fired", "x.ai/scheduled_task_inject_prompt", "x.ai/scheduled_task_fired"} {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message["method"] != method {
			t.Fatalf("message %d=%#v", index, message)
		}
	}
	current.mu.Lock()
	if len(current.wakeQueue) != 1 {
		current.mu.Unlock()
		t.Fatalf("wake queue=%#v", current.wakeQueue)
	}
	current.running = false
	current.mu.Unlock()
	server.startNextWake(current)
	server.wg.Wait()
	streamer.mu.Lock()
	if len(streamer.requests) != 1 || streamer.requests[0].PreviousResponseID != "parent-response" {
		streamer.mu.Unlock()
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[0].Input)
	streamer.mu.Unlock()
	if !strings.Contains(string(input), "check deployment") {
		t.Fatalf("scheduled input=%s", input)
	}

	created, err := registry.Execute(context.Background(), "scheduler_create", json.RawMessage(`{"interval":"1h","prompt":"later"}`))
	if err != nil {
		t.Fatal(err)
	}
	var createdOutput map[string]any
	if err := json.Unmarshal([]byte(created), &createdOutput); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	params, _ := json.Marshal(map[string]any{"sessionId": "parent-scheduler", "taskId": createdOutput["id"]})
	server.handleScheduler(message{ID: json.RawMessage("1"), Method: "x.ai/scheduler/delete", Params: params})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)["result"].(map[string]any)
	if result["taskId"] != createdOutput["id"] || result["deleted"] != true {
		t.Fatalf("delete response=%#v", response)
	}
}

func TestSubagentGetListRunningAndCancelWireContract(t *testing.T) {
	results := map[string]tools.SubagentResult{
		"running-1": {ID: "running-1", Type: "explore", Description: "find code", Status: "running", StartedAtMS: 10, DurationMS: 20, Turns: 2, ToolCalls: 3, TokensUsed: 1200, ContextWindow: 256000, ContextUsage: 40, ToolsUsed: []string{"read_file", "grep"}, ErrorCount: 1},
		"done-1":    {ID: "done-1", Type: "general-purpose", Description: "implement", Status: "completed", Output: "done", ToolCalls: 3, Turns: 2, TokensUsed: 2400, ContextWindow: 128000, ContextUsage: 50, ToolsUsed: []string{"write_file"}, StartedAtMS: 30, DurationMS: 40},
	}
	var getTimeout time.Duration
	current := &session{id: "parent-1", runner: &agent.Runner{
		ListSubagents: func() []tools.SubagentResult { return []tools.SubagentResult{results["running-1"], results["done-1"]} },
		GetSubagent: func(_ context.Context, id string, timeout time.Duration) (tools.SubagentResult, error) {
			getTimeout = timeout
			result, ok := results[id]
			if !ok {
				return tools.SubagentResult{}, errors.New("not found")
			}
			return result, nil
		},
		KillSubagent: func(_ context.Context, id string) (string, error) {
			if results[id].Status != "running" {
				return "already_finished", nil
			}
			return "killed", nil
		},
	}}
	request := func(method, params string) map[string]any {
		var output bytes.Buffer
		server := &Server{output: &output, sessions: map[string]*session{"parent-1": current}}
		server.handleSubagents(context.Background(), message{ID: json.RawMessage("1"), Method: method, Params: json.RawMessage(params)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response["result"].(map[string]any)
	}

	listed := request("x.ai/subagent/list_running", `{"sessionId":"parent-1"}`)
	items := listed["result"].(map[string]any)["subagents"].([]any)
	running := items[0].(map[string]any)
	if len(items) != 1 || running["subagentId"] != "running-1" || running["parentSessionId"] != "parent-1" || running["contextWindowTokens"] != float64(256000) || running["tokensUsed"] != float64(1200) || running["contextUsagePct"] != float64(40) || running["errorCount"] != float64(1) || len(running["toolsUsed"].([]any)) != 2 {
		t.Fatalf("listed=%#v", listed)
	}
	got := request("x.ai/subagent/get", `{"subagentId":"done-1","block":true,"timeoutMs":25}`)
	snapshot := got["result"].(map[string]any)["snapshot"].(map[string]any)
	if snapshot["status"] != "completed" || snapshot["output"] != "done" || snapshot["toolCalls"] != float64(3) || snapshot["tokensUsed"] != float64(2400) || snapshot["contextUsagePct"] != float64(50) || len(snapshot["toolsUsed"].([]any)) != 1 || getTimeout != 25*time.Millisecond {
		t.Fatalf("snapshot=%#v timeout=%s", snapshot, getTimeout)
	}
	cancelled := request("x.ai/subagent/cancel", `{"subagentId":"done-1"}`)
	cancelResult := cancelled["result"].(map[string]any)
	if cancelResult["cancelled"] != false || cancelResult["outcome"].(map[string]any)["kind"] != "already_finished" || cancelResult["outcome"].(map[string]any)["status"] != "completed" {
		t.Fatalf("cancelled=%#v", cancelled)
	}
	missing := request("x.ai/subagent/get", `{"subagentId":"missing"}`)
	if missing["result"].(map[string]any)["snapshot"] != nil {
		t.Fatalf("missing=%#v", missing)
	}
}

func TestPluginActionUpdatesInventoryAndSkills(t *testing.T) {
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: Review\n---\nReview.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"name":"review-tools"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".mcp.json"), []byte(`{"mcpServers":{"review":{"command":"review"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, ".lsp.json"), []byte(`{"review":{"command":"review-lsp"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := plugin.Settings{}
	var inventory []plugin.Plugin
	runner := &agent.Runner{Skills: catalog}
	runner.PluginInventory = func() []plugin.Plugin { return append([]plugin.Plugin(nil), inventory...) }
	runner.UpdatePlugins = func(_ context.Context, update func(*plugin.Settings)) ([]plugin.Plugin, error) {
		if update != nil {
			update(&settings)
		}
		inventory, err = plugin.Inventory(root, plugin.Config{
			Paths: settings.Paths, Enabled: settings.Enabled, Disabled: settings.Disabled, ProjectTrusted: true,
		})
		if err == nil {
			err = catalog.ReconfigurePlugins(enabledPluginFixtures(inventory))
		}
		return append([]plugin.Plugin(nil), inventory...), err
	}
	current := &session{id: "plugin-action", cwd: root, runner: runner}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"plugin-action": current}}

	server.handlePlugins(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/plugins/action", Params: json.RawMessage(`{"sessionId":"plugin-action","action":{"type":"add","path":` + strconv.Quote(pluginRoot) + `}}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	outcome := response["result"].(map[string]any)["result"].(map[string]any)
	if outcome["status"] != "success" || outcome["requiresRestart"] != false || len(inventory) != 1 || strings.Join(catalog.Names(), "|") != "review-tools:review" {
		t.Fatalf("unexpected add outcome=%#v inventory=%#v skills=%#v", outcome, inventory, catalog.Names())
	}
	output.Reset()
	server.handlePlugins(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/plugins/action", Params: json.RawMessage(`{"sessionId":"plugin-action","action":{"type":"disable","plugin_id":` + strconv.Quote(inventory[0].ID) + `}}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	outcome = response["result"].(map[string]any)["result"].(map[string]any)
	if outcome["status"] != "success" || outcome["requiresRestart"] != false || inventory[0].Enabled || len(catalog.Names()) != 0 {
		t.Fatalf("unexpected disable outcome=%#v inventory=%#v skills=%#v", outcome, inventory, catalog.Names())
	}
}

func TestPluginActionInstallUpdateAndUninstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	source := filepath.Join(root, "source")
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(source, name, "skills", name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name, "skills", name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+name+"\n---\n"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		version := ""
		if name == "alpha" {
			version = `,"version":"1.0.0"`
		}
		if err := os.WriteFile(filepath.Join(source, name, "plugin.json"), []byte(`{"name":"`+name+`"`+version+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := plugin.Settings{}
	var inventory []plugin.Plugin
	runner := &agent.Runner{Skills: catalog}
	runner.PluginInventory = func() []plugin.Plugin { return append([]plugin.Plugin(nil), inventory...) }
	runner.UpdatePlugins = func(_ context.Context, update func(*plugin.Settings)) ([]plugin.Plugin, error) {
		if update != nil {
			update(&settings)
		}
		inventory, err = plugin.Inventory(root, plugin.Config{
			Paths: settings.Paths, Enabled: settings.Enabled, Disabled: settings.Disabled, ProjectTrusted: true,
		})
		if err == nil {
			err = catalog.ReconfigurePlugins(enabledPluginFixtures(inventory))
		}
		return append([]plugin.Plugin(nil), inventory...), err
	}
	current := &session{id: "plugin-lifecycle", cwd: root, runner: runner}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"plugin-lifecycle": current}}
	call := func(id, action string) map[string]any {
		t.Helper()
		server.handlePlugins(context.Background(), message{ID: json.RawMessage(id), Method: "x.ai/plugins/action", Params: json.RawMessage(`{"sessionId":"plugin-lifecycle","action":` + action + `}`)})
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		return response["result"].(map[string]any)["result"].(map[string]any)
	}

	outcome := call("1", `{"type":"install","source":`+strconv.Quote(source)+`}`)
	if outcome["status"] != "success" || len(inventory) != 2 || strings.Join(settings.Enabled, "|") != "alpha|beta" || strings.Join(catalog.Names(), "|") != "alpha:alpha|beta:beta" {
		t.Fatalf("install outcome=%#v inventory=%#v settings=%#v skills=%#v", outcome, inventory, settings, catalog.Names())
	}
	if err := os.WriteFile(filepath.Join(source, "alpha", "plugin.json"), []byte(`{"name":"alpha","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome = call("2", `{"type":"update","plugin_id":`+strconv.Quote(inventory[0].ID)+`}`)
	if outcome["status"] != "success" {
		t.Fatalf("update outcome=%#v", outcome)
	}
	alphaVersion := ""
	for _, item := range inventory {
		if item.Name == "alpha" {
			alphaVersion = item.Version
		}
	}
	if alphaVersion != "2.0.0" {
		t.Fatalf("updated inventory=%#v", inventory)
	}
	outcome = call("3", `{"type":"uninstall","plugin_id":"alpha"}`)
	if outcome["status"] != "confirmation_required" || len(inventory) != 2 {
		t.Fatalf("confirmation outcome=%#v inventory=%#v", outcome, inventory)
	}
	outcome = call("4", `{"type":"uninstall","plugin_id":"alpha","confirmed":true}`)
	if outcome["status"] != "success" || len(inventory) != 0 || len(settings.Enabled) != 0 || len(catalog.Names()) != 0 {
		t.Fatalf("uninstall outcome=%#v inventory=%#v settings=%#v skills=%#v", outcome, inventory, settings, catalog.Names())
	}
}

func TestPluginActionRejectsRunningSession(t *testing.T) {
	called := false
	runner := &agent.Runner{
		Skills: &skills.Catalog{},
		UpdatePlugins: func(context.Context, func(*plugin.Settings)) ([]plugin.Plugin, error) {
			called = true
			return nil, nil
		},
	}
	current := &session{id: "plugin-running", cwd: t.TempDir(), runner: runner, running: true}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"plugin-running": current}}
	server.handlePlugins(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/plugins/action", Params: json.RawMessage(`{"sessionId":"plugin-running","action":{"type":"reload"}}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	outcome := response["result"].(map[string]any)["result"].(map[string]any)
	if called || outcome["status"] != "validation_error" || !strings.Contains(outcome["message"].(string), "prompt is running") {
		t.Fatalf("unexpected running-session outcome=%#v called=%v", outcome, called)
	}
}

func TestMarketplaceExtensionsWireContract(t *testing.T) {
	called := false
	runner := &agent.Runner{
		MarketplaceList: func() ([]marketplace.ScanResult, error) {
			return []marketplace.ScanResult{{SourceName: "Local", SourceKind: "local", SourceURLOrPath: "/catalog", Plugins: []marketplace.Entry{{Name: "demo", RelativePath: "plugins/demo", InstallStatus: "not_installed", Components: &marketplace.Components{Skills: []marketplace.Component{{Name: "review", Description: "Review code"}}}}}}}, nil
		},
		MarketplaceAction: func(_ context.Context, action marketplace.Action) (marketplace.Outcome, error) {
			called = action.Type == "install" && action.SourceURLOrPath == "/catalog" && action.PluginRelativePath == "plugins/demo"
			return marketplace.Outcome{Status: "success", Message: "installed"}, nil
		},
	}
	current := &session{id: "marketplace", runner: runner}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"marketplace": current}}
	server.handleMarketplace(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/marketplace/list", Params: json.RawMessage(`{"sessionId":"marketplace"}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	sources := response["result"].(map[string]any)["result"].(map[string]any)["sources"].([]any)
	plugins := sources[0].(map[string]any)["plugins"].([]any)
	components := plugins[0].(map[string]any)["components"].(map[string]any)
	skills := components["skills"].([]any)
	if len(sources) != 1 || skills[0].(map[string]any)["name"] != "review" || skills[0].(map[string]any)["description"] != "Review code" {
		t.Fatalf("marketplace list=%#v", response)
	}
	output.Reset()
	server.handleMarketplace(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/marketplace/action", Params: json.RawMessage(`{"sessionId":"marketplace","action":{"type":"install","sourceUrlOrPath":"/catalog","pluginRelativePath":"plugins/demo"}}`)})
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	outcome := response["result"].(map[string]any)["result"].(map[string]any)
	if !called || outcome["status"] != "success" || outcome["requiresRestart"] != false {
		t.Fatalf("marketplace action=%#v called=%v", response, called)
	}
}

func enabledPluginFixtures(inventory []plugin.Plugin) []plugin.Plugin {
	var enabled []plugin.Plugin
	for _, item := range inventory {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	return enabled
}

type fixtureStreamer struct {
	mu       sync.Mutex
	results  []api.StreamResult
	requests []api.ResponseRequest
}

type blockingStreamer struct{ started chan struct{} }

func TestStartSessionAssignsRunnerSessionID(t *testing.T) {
	root := t.TempDir()
	server := &Server{
		SessionDir: t.TempDir(), sessions: make(map[string]*session),
		Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			ws, err := workspace.Open(cfg.CWD)
			if err != nil {
				return nil, nil, err
			}
			registry := tools.NewRegistry(ws, approver)
			return &agent.Runner{Tools: registry}, func() { _ = registry.Close() }, nil
		},
	}
	created, err := server.startSession(context.Background(), "session-123", SessionConfig{CWD: root}, "")
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			created.close()
		}
	}()
	if created.runner.SessionID != "session-123" {
		t.Fatalf("runner session ID=%q", created.runner.SessionID)
	}
	if created.runner.SessionPath != filepath.Join(server.SessionDir, "session-123.jsonl") {
		t.Fatalf("runner session path=%q", created.runner.SessionPath)
	}
	created.close()
	closed = true
	statePath := filepath.Join(server.SessionDir, "artifacts", "session-123", "hunks.json")
	if info, err := os.Stat(statePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("session hunk state was not persisted: %v", err)
	}
}

func TestCloseSessionWaitsForCancelledRun(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &blockingStreamer{started: make(chan struct{})}
	closed := make(chan struct{})
	current := &session{
		id: "closing", cwd: root, activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test"},
		close:  func() { close(closed) },
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"closing": current}}
	params, _ := json.Marshal(map[string]any{"sessionId": "closing", "prompt": []any{map[string]any{"type": "text", "text": "wait"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("1"), Method: "session/prompt", Params: params})
	select {
	case <-streamer.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	if !server.closeSession("closing") {
		t.Fatal("active session was not closed")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("session resources were not closed after the cancelled run completed")
	}
	server.wg.Wait()
}

func (f *blockingStreamer) StreamResponse(ctx context.Context, _ api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	close(f.started)
	<-ctx.Done()
	return api.StreamResult{}, ctx.Err()
}

func (f *fixtureStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	result := f.results[0]
	f.results = f.results[1:]
	f.mu.Unlock()
	if result.Text != "" && onText != nil {
		onText(result.Text)
	}
	return result, nil
}

func TestSessionForkContractAndModelResume(t *testing.T) {
	sessionDir, sourceCWD, newCWD := t.TempDir(), t.TempDir(), t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": sourceCWD, "modelId": "old-model"}},
		{"user_prompt", map[string]any{"text": "first"}},
		{"model_response", map[string]any{"text": "one", "response_id": "r1", "tool_call_count": 0}},
		{"user_prompt", map[string]any{"text": "second"}},
		{"model_response", map[string]any{"text": "two", "response_id": "r2", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	_ = logger.Close()
	configs := make(chan SessionConfig, 1)
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, _ tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		configs <- cfg
		return nil, nil, errors.New("stop after config capture")
	}}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder, decoder := json.NewEncoder(clientToAgentW), json.NewDecoder(agentToClientR)
	target := 0
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/session/fork",
		"params": map[string]any{
			"sourceSessionId": "parent", "sourceCwd": sourceCWD, "newCwd": newCWD,
			"newSessionId": "child", "newModelId": "new-model", "targetPromptIndex": target,
		},
	})
	forked := decodeACP(t, decoder)
	result := forked["result"].(map[string]any)
	if result["newSessionId"] != "child" || result["parentSessionId"] != "parent" || result["newModelId"] != "new-model" || result["chatMessagesCopied"].(float64) != 2 {
		t.Fatalf("unexpected fork response: %#v", forked)
	}
	items, err := sessionlog.List(sessionDir, newCWD)
	if err != nil || len(items) != 1 || items[0].ModelID != "new-model" {
		t.Fatalf("fork metadata: %#v err=%v", items, err)
	}
	path, _ := sessionlog.PathForID(sessionDir, "child")
	messages, err := sessionlog.Transcript(path)
	if err != nil || len(messages) != 2 || messages[1].Text != "one" {
		t.Fatalf("fork transcript: %#v err=%v", messages, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/resume",
		"params": map[string]any{"sessionId": "child", "cwd": newCWD},
	})
	if cfg := <-configs; cfg.Model != "new-model" || cfg.ResumePath != path {
		t.Fatalf("fork model was not resumed: %#v", cfg)
	}
	_ = decodeACP(t, decoder)
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestACPStdioLifecycleStreamingAndPermission(t *testing.T) {
	root := t.TempDir()
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = root
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "response-1", ToolCalls: []api.ToolCall{{
			CallID: "tool-1", Name: "write_file", Arguments: json.RawMessage(`{"path":"made.txt","content":"ok"}`),
		}}},
		{ResponseID: "response-2", Text: "finished"},
		{ResponseID: "response-3", Text: "replacement answer"},
		{ResponseID: "response-4", Text: "compacted replacement"},
	}}
	factoryConfigs := make(chan SessionConfig, 1)
	sessionDir := t.TempDir()
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		factoryConfigs <- cfg
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		logger, err := sessionlog.NewLoggerWithID(sessionDir, cfg.SessionID)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		if err := logger.Append("session_metadata", map[string]any{"cwd": cfg.CWD}); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		return &agent.Runner{Client: streamer, Tools: registry, Logger: logger, Model: "fixture", MaxSteps: 3, TextOutput: text, StatusOutput: status}, func() {
			_ = logger.Close()
			_ = registry.Close()
		}, nil
	}}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder := json.NewEncoder(clientToAgentW)
	decoder := json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	initialize := decodeACP(t, decoder)
	if int(initialize["id"].(float64)) != 1 {
		t.Fatalf("unexpected initialize response: %#v", initialize)
	}
	promptCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["promptCapabilities"].(map[string]any)
	if promptCapabilities["embeddedContext"] != true || promptCapabilities["image"] != true || promptCapabilities["audio"] != false {
		t.Fatalf("unexpected prompt capabilities: %#v", promptCapabilities)
	}
	mcpCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["mcpCapabilities"].(map[string]any)
	if mcpCapabilities["http"] != true || mcpCapabilities["sse"] != true {
		t.Fatalf("unexpected MCP capabilities: %#v", mcpCapabilities)
	}
	sessionCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := sessionCapabilities["list"]; !ok || initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["loadSession"] != true {
		t.Fatalf("session list capability missing: %#v", sessionCapabilities)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
		"_meta": map[string]any{"yoloMode": true, "autoMode": true},
		"cwd":   root, "mcpServers": []any{map[string]any{
			"name": "client-tools", "command": "/fixture-mcp", "args": []string{"--stdio"},
			"env": []any{map[string]any{"name": "TOKEN", "value": "secret"}},
		}, map[string]any{
			"type": "http", "name": "remote-http", "url": "https://mcp.example/rpc",
			"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer token"}},
		}, map[string]any{
			"type": "sse", "name": "remote-sse", "url": "https://mcp.example/sse",
		}},
	}})
	created := decodeACP(t, decoder)
	receivedConfig := <-factoryConfigs
	if receivedConfig.YoloMode == nil || !*receivedConfig.YoloMode || receivedConfig.AutoMode == nil || !*receivedConfig.AutoMode {
		t.Fatalf("session permission metadata was not forwarded: %#v", receivedConfig)
	}
	if len(receivedConfig.MCPServers) != 3 || receivedConfig.MCPServers[0].Env["TOKEN"] != "secret" ||
		receivedConfig.MCPServers[1].Type != "http" || receivedConfig.MCPServers[1].Headers["Authorization"] != "Bearer token" ||
		receivedConfig.MCPServers[2].Type != "sse" {
		t.Fatalf("client MCP config was not forwarded: %#v", receivedConfig)
	}
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	modes := created["result"].(map[string]any)["modes"].(map[string]any)
	if modes["currentModeId"] != "default" || len(modes["availableModes"].([]any)) != 3 {
		t.Fatalf("unexpected session modes: %#v", modes)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 22, "method": "session/list", "params": map[string]any{"cwd": root}})
	listed := decodeACP(t, decoder)
	sessions := listed["result"].(map[string]any)["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["sessionId"] != sessionID {
		t.Fatalf("unexpected session list: %#v", listed)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 23, "method": "session/set_mode", "params": map[string]any{
		"sessionId": sessionID, "modeId": "plan",
	}})
	modeUpdate := decodeACP(t, decoder)
	modeData := modeUpdate["params"].(map[string]any)["update"].(map[string]any)
	if modeData["sessionUpdate"] != "current_mode_update" || modeData["currentModeId"] != "plan" {
		t.Fatalf("unexpected mode update: %#v", modeUpdate)
	}
	if response := decodeACP(t, decoder); int(response["id"].(float64)) != 23 {
		t.Fatalf("unexpected set mode response: %#v", response)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 24, "method": "session/set_mode", "params": map[string]any{
		"sessionId": sessionID, "modeId": "default",
	}})
	_ = decodeACP(t, decoder)
	if response := decodeACP(t, decoder); int(response["id"].(float64)) != 24 {
		t.Fatalf("unexpected set default mode response: %#v", response)
	}
	for _, want := range []string{"plan", "default"} {
		encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "method": "x.ai/toggle_plan_mode", "params": map[string]any{"sessionId": sessionID}})
		modeUpdate := decodeACP(t, decoder)
		modeData := modeUpdate["params"].(map[string]any)["update"].(map[string]any)
		if modeData["sessionUpdate"] != "current_mode_update" || modeData["currentModeId"] != want {
			t.Fatalf("unexpected toggle mode update: %#v", modeUpdate)
		}
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "create the file"}},
	}})
	titleUpdate := decodeACP(t, decoder)
	infoUpdate := titleUpdate["params"].(map[string]any)["update"].(map[string]any)
	if infoUpdate["sessionUpdate"] != "session_info_update" || infoUpdate["title"] != "create the file" {
		t.Fatalf("unexpected session info update: %#v", titleUpdate)
	}
	toolStarted := decodeACPNonQueue(t, decoder)
	startedUpdate := toolStarted["params"].(map[string]any)["update"].(map[string]any)
	if startedUpdate["sessionUpdate"] != "tool_call" || startedUpdate["toolCallId"] != "tool-1" {
		t.Fatalf("unexpected tool start: %#v", toolStarted)
	}
	permission := decodeACPNonQueue(t, decoder)
	if permission["method"] != "session/request_permission" {
		t.Fatalf("unexpected permission request: %#v", permission)
	}
	permissionTool := permission["params"].(map[string]any)["toolCall"].(map[string]any)
	if permissionTool["toolCallId"] != "tool-1" {
		t.Fatalf("permission did not reference tool call: %#v", permission)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": permission["id"],
		"result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}},
	})
	toolFinished := decodeACPNonQueue(t, decoder)
	finishedUpdate := toolFinished["params"].(map[string]any)["update"].(map[string]any)
	if finishedUpdate["sessionUpdate"] != "tool_call_update" || finishedUpdate["status"] != "completed" {
		t.Fatalf("unexpected tool finish: %#v", toolFinished)
	}
	textUpdate := decodeACPNonQueue(t, decoder)
	if textUpdate["method"] != "session/update" {
		t.Fatalf("unexpected stream update: %#v", textUpdate)
	}
	promptComplete := decodeACPNonQueue(t, decoder)
	assertPromptComplete(t, promptComplete, sessionID, "", "end_turn")
	completed := decodeACPNonQueue(t, decoder)
	if int(completed["id"].(float64)) != 3 || completed["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("unexpected prompt response: %#v", completed)
	}
	data, err := os.ReadFile(filepath.Join(root, "made.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("tool did not run: data=%q err=%v", data, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 33, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "path": "made.txt", "source": "agent"},
	})
	hunkResponse := decodeACPNonQueue(t, decoder)
	hunkResult := hunkResponse["result"].(map[string]any)
	hunks := hunkResult["hunks"].([]any)
	if len(hunks) != 1 {
		t.Fatalf("unexpected ACP hunks: %#v", hunkResponse)
	}
	hunk := hunks[0].(map[string]any)
	hunkSource := hunk["source"].(map[string]any)
	lineInfo := hunk["lineInfo"].(map[string]any)
	if hunk["path"] != filepath.Join(root, "made.txt") || hunkSource["type"] != "agentEdit" || int(hunkSource["prompt_index"].(float64)) != 0 || int(lineInfo["newStart"].(float64)) != 1 || hunk["patch"] == nil {
		t.Fatalf("unexpected ACP hunks: %#v", hunkResponse)
	}
	if hunkResult["baseline"].(map[string]any)["status"] != "missing" || hunkResult["current"].(map[string]any)["content"] != "ok" || hunkResult["currentContent"] != "ok" {
		t.Fatalf("unexpected ACP file content: %#v", hunkResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 331, "method": "x.ai/hunk-tracker/get-summary",
		"params": map[string]any{"sessionId": sessionID},
	})
	summaryResult := decodeACP(t, decoder)["result"].(map[string]any)
	summaryStats := summaryResult["stats"].(map[string]any)
	turns := summaryResult["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("unexpected ACP hunk summary: %#v", summaryResult)
	}
	turnHunk := turns[0].(map[string]any)["pendingHunks"].([]any)[0].(map[string]any)
	if int(summaryResult["filesModified"].(float64)) != 1 || int(summaryResult["pendingHunks"].(float64)) != 1 || int(summaryStats["acceptedHunks"].(float64)) != 0 || turnHunk["path"] != filepath.Join(root, "made.txt") || turnHunk["patch"] != nil {
		t.Fatalf("unexpected ACP hunk summary: %#v", summaryResult)
	}
	if _, exists := summaryResult["fileCount"]; exists {
		t.Fatalf("ACP hunk summary included non-reference fields: %#v", summaryResult)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 332, "method": "x.ai/hunk-tracker/get-files",
		"params": map[string]any{"sessionId": sessionID},
	})
	fileItems := decodeACP(t, decoder)["result"].(map[string]any)["files"].([]any)
	if len(fileItems) != 1 || fileItems[0].(map[string]any)["path"] != filepath.Join(root, "made.txt") || fileItems[0].(map[string]any)["isAgentFile"] != true {
		t.Fatalf("unexpected ACP hunk files: %#v", fileItems)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 333, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "source": "future-source"},
	})
	allHunkResult := decodeACP(t, decoder)["result"].(map[string]any)
	allHunks := allHunkResult["hunks"].([]any)
	if len(allHunks) != 1 || allHunks[0].(map[string]any)["patch"] != nil {
		t.Fatalf("unexpected unfiltered ACP hunks: %#v", allHunkResult)
	}
	if _, exists := allHunkResult["baseline"]; exists {
		t.Fatalf("all-hunks response included file content: %#v", allHunkResult)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 34, "method": "x.ai/hunk-tracker/turn-action",
		"params": map[string]any{"sessionId": sessionID, "promptIndex": 0, "action": "accept"},
	})
	actionResponse := decodeACP(t, decoder)
	actionResult := actionResponse["result"].(map[string]any)
	if actionResult["success"] != true || int(actionResult["affectedCount"].(float64)) != 1 {
		t.Fatalf("unexpected turn action response: %#v", actionResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 341, "method": "x.ai/hunk-tracker/hunk-action",
		"params": map[string]any{"sessionId": sessionID, "hunkId": hunk["id"], "action": "accept"},
	})
	alreadyAccepted := decodeACP(t, decoder)["result"].(map[string]any)
	if alreadyAccepted["success"] != false || alreadyAccepted["error"] == nil {
		t.Fatalf("accepted hunk action did not fail closed: %#v", alreadyAccepted)
	}
	if _, exists := alreadyAccepted["affectedCount"]; exists {
		t.Fatalf("failed action included affectedCount: %#v", alreadyAccepted)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 35, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "source": "all"},
	})
	acceptedResponse := decodeACP(t, decoder)
	if visible := acceptedResponse["result"].(map[string]any)["hunks"].([]any); len(visible) != 0 {
		t.Fatalf("accepted ACP hunk remained visible: %#v", acceptedResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 351, "method": "x.ai/hunk-tracker/get-all-file-contents",
		"params": map[string]any{"sessionId": sessionID},
	})
	contentResponse := decodeACP(t, decoder)
	contentFiles := contentResponse["result"].(map[string]any)["files"].([]any)
	if len(contentFiles) != 1 || contentFiles[0].(map[string]any)["path"] != filepath.Join(root, "made.txt") || contentFiles[0].(map[string]any)["current"].(map[string]any)["content"] != "ok" || contentFiles[0].(map[string]any)["isAgentFile"] != true {
		t.Fatalf("unexpected all-file contents: %#v", contentResponse)
	}
	if err := os.WriteFile(filepath.Join(root, "made.txt"), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 36, "method": "x.ai/rewind/points",
		"params": map[string]any{"sessionId": sessionID},
	})
	pointsResponse := decodeACP(t, decoder)
	points := pointsResponse["result"].(map[string]any)["rewind_points"].([]any)
	if len(points) != 1 {
		t.Fatalf("unexpected ACP rewind points: %#v", pointsResponse)
	}
	point := points[0].(map[string]any)
	if int(point["prompt_index"].(float64)) != 0 || point["has_file_changes"] != true || int(point["num_file_snapshots"].(float64)) != 1 {
		t.Fatalf("unexpected ACP rewind points: %#v", pointsResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 37, "method": "x.ai/rewind/execute",
		"params": map[string]any{"sessionId": sessionID, "targetPromptIndex": 0, "force": false, "mode": "all"},
	})
	preview := decodeACP(t, decoder)
	previewResult := preview["result"].(map[string]any)
	if previewResult["success"] != false || len(previewResult["clean_files"].([]any)) != 0 || len(previewResult["conflicts"].([]any)) != 1 || !strings.Contains(previewResult["error"].(string), "External modifications") {
		t.Fatalf("unexpected all-mode rewind preview: %#v", preview)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 38, "method": "x.ai/rewind/execute",
		"params": map[string]any{"sessionId": sessionID, "targetPromptIndex": 0, "force": true, "mode": "all"},
	})
	rewindUpdate := decodeACP(t, decoder)
	if rewindUpdate["method"] != "session/update" || rewindUpdate["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "rewind_marker" {
		t.Fatalf("missing ACP rewind marker: %#v", rewindUpdate)
	}
	rewound := decodeACP(t, decoder)
	if rewound["result"].(map[string]any)["success"] != true || rewound["result"].(map[string]any)["prompt_text"] != "create the file" || len(rewound["result"].(map[string]any)["reverted_files"].([]any)) != 1 {
		t.Fatalf("unexpected ACP rewind response: %#v", rewound)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !os.IsNotExist(err) {
		t.Fatalf("all-mode rewind did not restore files: %v", err)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 25, "method": "session/set_mode", "params": map[string]any{
		"sessionId": sessionID, "modeId": "plan",
	}})
	_ = decodeACP(t, decoder)
	if response := decodeACP(t, decoder); int(response["id"].(float64)) != 25 {
		t.Fatalf("unexpected restore plan mode response: %#v", response)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 39, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "replacement"}}},
	})
	replacementUpdate := decodeACPNonQueue(t, decoder)
	if replacementUpdate["method"] != "session/update" {
		t.Fatalf("missing replacement stream update: %#v", replacementUpdate)
	}
	replacementComplete := decodeACPNonQueue(t, decoder)
	assertPromptComplete(t, replacementComplete, sessionID, "", "end_turn")
	replacementDone := decodeACPNonQueue(t, decoder)
	if int(replacementDone["id"].(float64)) != 39 {
		t.Fatalf("unexpected replacement completion: %#v", replacementDone)
	}
	streamer.mu.Lock()
	if len(streamer.requests) != 3 || streamer.requests[2].PreviousResponseID != "" || !strings.Contains(streamer.requests[2].Instructions, "Plan mode is active.") {
		t.Fatalf("rewound prompt used the discarded response chain: %#v", streamer.requests)
	}
	streamer.mu.Unlock()
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 40, "method": "x.ai/compact_conversation",
		"params": map[string]any{"sessionId": sessionID, "userContext": "retain the replacement plan"},
	})
	compactUpdate := decodeACPNonQueue(t, decoder)
	compactUpdateParams := compactUpdate["params"].(map[string]any)
	if compactUpdate["method"] != "x.ai/session_notification" || compactUpdateParams["update"].(map[string]any)["sessionUpdate"] != "auto_compact_completed" {
		t.Fatalf("missing compact extension update: %#v", compactUpdate)
	}
	compactDone := decodeACPNonQueue(t, decoder)
	if int(compactDone["id"].(float64)) != 40 || len(compactDone["result"].(map[string]any)) != 0 {
		t.Fatalf("unexpected compact extension response: %#v", compactDone)
	}
	streamer.mu.Lock()
	if len(streamer.requests) != 4 || streamer.requests[3].PreviousResponseID != "response-3" || !strings.Contains(streamer.requests[3].Input[0].Content.(string), "retain the replacement plan") {
		t.Fatalf("compact extension was not routed: %#v", streamer.requests)
	}
	streamer.mu.Unlock()
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/close", "params": map[string]any{"sessionId": sessionID}})
	closed := decodeACPNonQueue(t, decoder)
	if int(closed["id"].(float64)) != 4 {
		t.Fatalf("unexpected close response: %#v", closed)
	}
	path, err := sessionlog.PathForID(sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := sessionlog.CurrentMode(path); err != nil || mode != "plan" {
		t.Fatalf("persisted mode=%q err=%v", mode, err)
	}
	_ = clientToAgentW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop at EOF")
	}
}

func TestParseMCPServersRejectsInvalidWireValues(t *testing.T) {
	tests := []string{
		`[{"name":"missing-command"}]`,
		`[{"type":"http","name":"bad-url","url":"file:///tmp/socket"}]`,
		`[{"type":"sse","name":"bad-header","url":"https://example.com/sse","headers":[{"name":"Bad Header","value":"x"}]}]`,
		`[{"type":"http","name":"bad-value","url":"https://example.com/mcp","headers":[{"name":"X-Test","value":"x\r\ny"}]}]`,
		`[{"type":"websocket","name":"unknown"}]`,
	}
	for _, raw := range tests {
		var params []mcpServerParam
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatal(err)
		}
		if _, err := parseMCPServers(params); err == nil {
			t.Errorf("invalid MCP servers were accepted: %s", raw)
		}
	}
}

func TestRenderPromptSupportsEmbeddedTextAndImages(t *testing.T) {
	var embedded promptBlock
	embedded.Type = "resource"
	embedded.Resource.URI = "file:///workspace/context.md"
	embedded.Resource.MimeType = "text/markdown"
	embedded.Resource.Text = "# Context"
	prompt, content, err := renderPrompt([]promptBlock{
		{Type: "text", Text: "Use this context"},
		embedded,
		{Type: "resource_link", Name: "spec", URI: "file:///workspace/spec.md"},
		{Type: "image", MimeType: "image/png", Data: "aGVsbG8="},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Embedded resource file:///workspace/context.md (text/markdown):\n# Context") {
		t.Fatalf("embedded resource missing from prompt: %q", prompt)
	}
	if len(content) != 4 || content[3].Type != "input_image" || content[3].ImageURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image missing from prompt content: %#v", content)
	}
	_, remote, err := renderPrompt([]promptBlock{{Type: "image", URI: "https://example.com/image.png"}})
	if err != nil || len(remote) != 1 || remote[0].ImageURL != "https://example.com/image.png" {
		t.Fatalf("remote image was not preserved: content=%#v err=%v", remote, err)
	}
	for _, block := range []promptBlock{
		{Type: "image"},
		{Type: "image", MimeType: "image/svg+xml", Data: "PHN2Zz4="},
		{Type: "image", MimeType: "image/png", Data: "not-base64"},
		{Type: "audio"},
	} {
		if _, _, err := renderPrompt([]promptBlock{block}); err == nil {
			t.Errorf("expected unsupported media error for %#v", block)
		}
	}
	var blob promptBlock
	blob.Type = "resource"
	blob.Resource.URI = "file:///workspace/data.bin"
	blob.Resource.Blob = "AA=="
	if _, _, err := renderPrompt([]promptBlock{blob}); err == nil {
		t.Fatal("expected unsupported binary resource error")
	}
}

func TestACPToolUpdateIncludesImageContent(t *testing.T) {
	var output strings.Builder
	server := &Server{output: &output}
	observer := &sessionToolObserver{server: server, sessionID: "session-1"}
	observer.ToolFinished(api.ToolCall{CallID: "call-1"}, tools.ExecutionResult{
		Output: "[PDF: doc.pdf (2 pages rendered, 2 total)]",
		Images: []tools.ImageAttachment{
			{MediaType: "image/jpeg", Data: []byte("page-one")},
			{MediaType: "image/jpeg", Data: []byte("page-two")},
		},
	}, nil)
	var notification map[string]any
	if err := json.Unmarshal([]byte(output.String()), &notification); err != nil {
		t.Fatal(err)
	}
	update := notification["params"].(map[string]any)["update"].(map[string]any)
	content := update["content"].([]any)
	if len(content) != 2 || update["status"] != "completed" {
		t.Fatalf("unexpected tool update: %#v", update)
	}
	first := content[0].(map[string]any)
	image := first["content"].(map[string]any)
	if first["type"] != "content" || image["type"] != "image" || image["mimeType"] != "image/jpeg" || image["data"] != base64.StdEncoding.EncodeToString([]byte("page-one")) {
		t.Fatalf("unexpected image content: %#v", first)
	}
}

func TestWorktreeExtensionsCreateListShowAndRemove(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt", ".gitignore")
	runACPGit(t, root, "commit", "-qm", "baseline")
	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "skip.log"), []byte("skip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "worktree")

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	server := &Server{
		SessionDir: t.TempDir(),
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, errors.New("session factory should not be called")
		},
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder := json.NewEncoder(clientToAgentW)
	decoder := json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/git/worktree/create",
		"params": map[string]any{
			"sessionId": "wt-session", "sourcePath": root, "worktreePath": dest,
			"copyMode": "clean", "worktreeType": "linked", "label": "ACP Test",
			"copyIgnoredInBackground": true, "ignoredSkipPatterns": []string{"ignored/skip.log"},
		},
	})
	created := decodeACP(t, decoder)
	createdResult := created["result"].(map[string]any)
	if createdResult["status"] != "creating" || createdResult["worktreePath"] != dest {
		t.Fatalf("unexpected create response: %#v", created)
	}
	notification := decodeACP(t, decoder)
	if notification["method"] != "x.ai/git/worktree/status" || notification["params"].(map[string]any)["status"] != "created" {
		t.Fatalf("unexpected worktree notification: %#v", notification)
	}
	copyingIgnored := decodeACP(t, decoder)
	if copyingIgnored["params"].(map[string]any)["status"] != "copyingIgnored" {
		t.Fatalf("unexpected ignored-copy start: %#v", copyingIgnored)
	}
	ignoredComplete := decodeACP(t, decoder)
	completeParams := ignoredComplete["params"].(map[string]any)
	if completeParams["status"] != "ignoredCopyComplete" || int(completeParams["filesCopied"].(float64)) != 1 {
		t.Fatalf("unexpected ignored-copy completion: %#v", ignoredComplete)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "ignored", "keep.txt")); err != nil || string(data) != "keep\n" {
		t.Fatalf("ignored file was not copied: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ignored", "skip.log")); !os.IsNotExist(err) {
		t.Fatalf("skipped ignored file was copied: %v", err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 11, "method": "x.ai/git/worktree/create",
		"params": map[string]any{
			"sessionId": "wt-session", "sourcePath": root, "worktreePath": dest,
			"copyMode": "clean", "worktreeType": "linked", "label": "ACP Test",
		},
	})
	existing := decodeACP(t, decoder)["result"].(map[string]any)
	if existing["status"] != "exists" || existing["commit"] == nil {
		t.Fatalf("unexpected existing worktree response: %#v", existing)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/git/worktree/list", "params": map[string]any{},
	})
	listed := decodeACP(t, decoder)
	records := listed["result"].([]any)
	if len(records) != 1 {
		t.Fatalf("unexpected worktree list: %#v", listed)
	}
	listedRecord := records[0].(map[string]any)
	if listedRecord["path"] != dest || listedRecord["session_id"] != "wt-session" || listedRecord["source_repo"] == nil || listedRecord["created_at"] == nil {
		t.Fatalf("unexpected worktree list: %#v", listed)
	}
	if _, exists := listedRecord["sessionId"]; exists {
		t.Fatalf("worktree list used non-reference field names: %#v", listedRecord)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "x.ai/git/worktree/show", "params": map[string]any{"idOrPath": dest},
	})
	shown := decodeACP(t, decoder)
	if shown["result"].(map[string]any)["session_id"] != "wt-session" {
		t.Fatalf("unexpected worktree show: %#v", shown)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "x.ai/git/worktree/apply",
		"params": map[string]any{"sessionId": "wt-session", "worktreePath": dest, "mode": "merge"},
	})
	applied := decodeACP(t, decoder)
	if applied["result"].(map[string]any)["status"] != "success" {
		t.Fatalf("unexpected worktree apply: %#v", applied)
	}
	if data, err := os.ReadFile(filepath.Join(root, "tracked.txt")); err != nil || string(data) != "applied\n" {
		t.Fatalf("ACP apply did not update source: %q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(dest, "fork-only.txt"), []byte("forked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 40, "method": "x.ai/git/worktree/create_from_worktree_sync",
		"params": map[string]any{
			"sourceWorktreePath": dest, "newSessionId": "fork-session", "copyMode": "dirty",
			"worktreeType": "linked", "label": "fork-child",
		},
	})
	forked := decodeACP(t, decoder)
	forkResult := forked["result"].(map[string]any)
	if forkResult["status"] != "created" || forkResult["newSessionId"] != "fork-session" {
		t.Fatalf("unexpected worktree fork: %#v", forked)
	}
	forkPath := forkResult["worktreePath"].(string)
	if data, err := os.ReadFile(filepath.Join(forkPath, "fork-only.txt")); err != nil || string(data) != "forked\n" {
		t.Fatalf("fork did not copy dirty state: %q err=%v", data, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 41, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": forkPath, "force": true},
	})
	if forkRemoved := decodeACP(t, decoder); forkRemoved["result"].(map[string]any)["removed"] != true {
		t.Fatalf("fork removal failed: %#v", forkRemoved)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 42, "method": "x.ai/git/worktree/db/stats", "params": map[string]any{}})
	stats := decodeACP(t, decoder)
	if stats["result"].(map[string]any)["total_records"].(float64) != 1 {
		t.Fatalf("unexpected worktree stats: %#v", stats)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 43, "method": "x.ai/git/worktree/db/path", "params": map[string]any{}})
	dbPath := decodeACP(t, decoder)
	if !strings.HasSuffix(dbPath["result"].(map[string]any)["path"].(string), "worktrees.json") {
		t.Fatalf("unexpected worktree DB path: %#v", dbPath)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 45, "method": "x.ai/git/worktree/db/rebuild", "params": map[string]any{}})
	rebuild := decodeACP(t, decoder)["result"].(map[string]any)
	if _, exists := rebuild["already_tracked"]; !exists {
		t.Fatalf("unexpected worktree rebuild report: %#v", rebuild)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 44, "method": "x.ai/git/worktree/gc",
		"params": map[string]any{"dryRun": true, "maxAge": "0s", "force": true},
	})
	gc := decodeACP(t, decoder)
	if gc["result"].(map[string]any)["expired_removed"].(float64) != 1 {
		t.Fatalf("unexpected worktree GC dry-run: %#v", gc)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("GC dry-run removed worktree: %v", err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": dest, "dryRun": true},
	})
	dryRun := decodeACP(t, decoder)
	if dryRun["result"].(map[string]any)["removed"] != false {
		t.Fatalf("unexpected worktree dry-run: %#v", dryRun)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": dest, "force": true},
	})
	removed := decodeACP(t, decoder)
	if removed["result"].(map[string]any)["removed"] != true {
		t.Fatalf("unexpected worktree remove: %#v", removed)
	}
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func runACPGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runACPGitOutput(t, dir, args...)
}

func runACPGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestSessionWorktreeResumeAndRehydrate(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "baseline")
	historicalHead := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD"))
	sessionDir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "resume-parent")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": root, "headCommit": historicalHead})
	_ = logger.Append("user_prompt", map[string]any{"text": "resume me"})
	_ = logger.Append("model_response", map[string]any{"text": "ready", "response_id": "r1", "tool_call_count": 0})
	_ = logger.Close()
	if err := os.WriteFile(filepath.Join(root, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "later.txt")
	runACPGit(t, root, "commit", "-qm", "later")

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	server := &Server{SessionDir: sessionDir, Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, errors.New("session factory should not be called")
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder, decoder := json.NewEncoder(clientToAgentW), json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 0, "method": "x.ai/session/resolve_local_for_worktree_resume",
		"params": map[string]any{"sessionId": "resume-parent", "cwd": root},
	})
	resolved := decodeACP(t, decoder)
	if result := resolved["result"].(map[string]any); result["found"] != true || result["resolutionKind"] != "exactCwd" {
		t.Fatalf("unexpected local resolution: %#v", resolved)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/git/worktree/resume_session",
		"params": map[string]any{"sessionId": "resume-parent", "sourceCwd": root, "copyMode": "clean", "worktreeType": "linked", "restoreCode": true},
	})
	resumed := decodeACP(t, decoder)
	result := resumed["result"].(map[string]any)
	if result["parentSessionId"] != "resume-parent" || result["remoteRestored"] != false || result["chatMessagesCopied"].(float64) != 2 {
		t.Fatalf("unexpected resume response: %#v", resumed)
	}
	resumedID, resumedPath := result["sessionId"].(string), result["worktreePath"].(string)
	if result["codeRestored"] != true || result["restoreDegree"] != "head_only" || strings.TrimSpace(runACPGitOutput(t, resumedPath, "rev-parse", "HEAD")) != historicalHead {
		t.Fatalf("historical HEAD was not restored: %#v", resumed)
	}
	if items, err := sessionlog.List(sessionDir, result["effectiveCwd"].(string)); err != nil || len(items) != 1 || items[0].SessionID != resumedID {
		t.Fatalf("forked session not loadable: %#v err=%v", items, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "x.ai/session/resolve_local_for_worktree_resume",
		"params": map[string]any{"sessionId": "resume-parent", "cwd": resumedPath},
	})
	resolvedSibling := decodeACP(t, decoder)
	if result := resolvedSibling["result"].(map[string]any); result["found"] != true || result["resolutionKind"] != "sameRepoDifferentCwd" {
		t.Fatalf("unexpected sibling resolution: %#v", resolvedSibling)
	}
	rehydratedPath := filepath.Join(t.TempDir(), "rehydrated")
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/session/rehydrate",
		"params": map[string]any{"sessionId": "resume-parent", "sourceCwd": rehydratedPath, "repoRoot": root, "worktreePath": rehydratedPath},
	})
	rehydrated := decodeACP(t, decoder)
	if rehydrated["result"].(map[string]any)["codebaseRestored"] != true {
		t.Fatalf("unexpected rehydrate response: %#v", rehydrated)
	}
	if _, err := os.Stat(filepath.Join(rehydratedPath, "tracked.txt")); err != nil {
		t.Fatalf("rehydrated worktree missing: %v", err)
	}
	for id, path := range map[int]string{3: resumedPath, 4: rehydratedPath} {
		encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": id, "method": "x.ai/git/worktree/remove", "params": map[string]any{"worktreePath": path, "force": true}})
		if response := decodeACP(t, decoder); response["result"].(map[string]any)["removed"] != true {
			t.Fatalf("cleanup failed: %#v", response)
		}
	}
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestACPCancelReturnsCancelledStopReason(t *testing.T) {
	root := t.TempDir()
	streamer := &blockingStreamer{started: make(chan struct{})}
	server := &Server{SessionDir: t.TempDir(), Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		return &agent.Runner{Client: streamer, Tools: registry, Model: "fixture", TextOutput: text, StatusOutput: status}, func() { _ = registry.Close() }, nil
	}}
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputR, outputW) }()
	encoder := json.NewEncoder(inputW)
	decoder := json.NewDecoder(outputR)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{"cwd": root, "mcpServers": []any{}}})
	created := decodeACP(t, decoder)
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "wait"}},
	}})
	titleUpdate := decodeACP(t, decoder)
	if titleUpdate["method"] != "session/update" {
		t.Fatalf("unexpected title update: %#v", titleUpdate)
	}
	select {
	case <-streamer.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	queueUpdate := decodeACP(t, decoder)
	if queueUpdate["method"] != "x.ai/queue/changed" {
		t.Fatalf("unexpected queue update: %#v", queueUpdate)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
	promptComplete := decodeACPNonQueue(t, decoder)
	assertPromptComplete(t, promptComplete, sessionID, "", "cancelled")
	completeParams := promptComplete["params"].(map[string]any)
	if completeParams["cancelTrigger"] != "ctrl_c" {
		t.Fatalf("unexpected cancel completion: %#v", promptComplete)
	}
	response := decodeACPNonQueue(t, decoder)
	if response["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("unexpected cancel response: %#v", response)
	}
	responseMeta := response["result"].(map[string]any)["_meta"].(map[string]any)
	if responseMeta["cancelTrigger"] != "ctrl_c" {
		t.Fatalf("unexpected cancel response metadata: %#v", response)
	}
	if completeParams["promptId"] == "" || completeParams["promptId"] != responseMeta["promptId"] {
		t.Fatalf("generated prompt ID did not correlate completion and response: %#v %#v", promptComplete, response)
	}
	emptyQueue := decodeACP(t, decoder)
	if emptyQueue["method"] != "x.ai/queue/changed" || len(emptyQueue["params"].(map[string]any)["entries"].([]any)) != 0 {
		t.Fatalf("unexpected final queue update: %#v", emptyQueue)
	}
	_ = inputW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop")
	}
}

func TestACPLoadReplaysAndResumeReconnectsPersistedSession(t *testing.T) {
	sessionDir := t.TempDir()
	workspaceRoot := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "persisted-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": workspaceRoot}); err != nil {
		t.Fatal(err)
	}
	imageData := base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	if err := logger.AppendPrompt("stored question", []sessionlog.Content{
		{Type: "text", Text: "stored question"},
		{Type: "image", URI: "data:image/png;base64," + imageData},
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "stored-response", "text": "stored answer", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_mode", map[string]any{"mode_id": "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	factoryConfigs := make(chan SessionConfig, 2)
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		factoryConfigs <- cfg
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		resumed, _, err := sessionlog.Resume(cfg.ResumePath)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		return &agent.Runner{
			Client: &fixtureStreamer{}, Tools: registry, Logger: resumed,
			Model: "fixture", TextOutput: text, StatusOutput: status,
		}, func() { _ = resumed.Close(); _ = registry.Close() }, nil
	}}
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputR, outputW) }()
	encoder := json.NewEncoder(inputW)
	decoder := json.NewDecoder(outputR)
	loadParams := map[string]any{
		"sessionId": "persisted-1", "cwd": workspaceRoot, "mcpServers": []any{},
		"_meta": map[string]any{"yoloMode": true, "auto_mode": false},
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/load", "params": loadParams})
	userTextReplay := decodeACP(t, decoder)
	userImageReplay := decodeACP(t, decoder)
	agentReplay := decodeACP(t, decoder)
	loaded := decodeACP(t, decoder)
	if userTextReplay["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "user_message_chunk" ||
		userImageReplay["params"].(map[string]any)["update"].(map[string]any)["content"].(map[string]any)["data"] != imageData ||
		agentReplay["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "agent_message_chunk" ||
		loaded["result"].(map[string]any)["sessionId"] != "persisted-1" {
		t.Fatalf("unexpected load sequence: %#v %#v %#v %#v", userTextReplay, userImageReplay, agentReplay, loaded)
	}
	if loaded["result"].(map[string]any)["modes"].(map[string]any)["currentModeId"] != "plan" {
		t.Fatalf("loaded mode was not restored: %#v", loaded)
	}
	loadedConfig := <-factoryConfigs
	if loadedConfig.YoloMode == nil || !*loadedConfig.YoloMode || loadedConfig.AutoMode == nil || *loadedConfig.AutoMode {
		t.Fatalf("load permission metadata was not forwarded: %#v", loadedConfig)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/close", "params": map[string]any{"sessionId": "persisted-1"}})
	_ = decodeACP(t, decoder)
	loadParams["_meta"] = map[string]any{"yoloMode": "invalid", "auto_mode": true}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/resume", "params": loadParams})
	resumed := decodeACP(t, decoder)
	if int(resumed["id"].(float64)) != 3 || resumed["result"].(map[string]any)["sessionId"] != "persisted-1" {
		t.Fatalf("unexpected resume response: %#v", resumed)
	}
	if resumed["result"].(map[string]any)["modes"].(map[string]any)["currentModeId"] != "plan" {
		t.Fatalf("resumed mode was not restored: %#v", resumed)
	}
	resumedConfig := <-factoryConfigs
	if resumedConfig.YoloMode != nil || resumedConfig.AutoMode == nil || !*resumedConfig.AutoMode {
		t.Fatalf("resume permission metadata was not parsed tolerantly: %#v", resumedConfig)
	}
	_ = inputW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop")
	}
}

func TestSessionPermissionModeOverrides(t *testing.T) {
	tests := []struct {
		name     string
		meta     map[string]any
		wantYolo *bool
		wantAuto *bool
	}{
		{name: "empty"},
		{name: "camel case", meta: map[string]any{"yoloMode": true, "autoMode": false}, wantYolo: boolPointer(true), wantAuto: boolPointer(false)},
		{name: "snake case", meta: map[string]any{"auto_mode": true}, wantAuto: boolPointer(true)},
		{name: "camel case takes precedence", meta: map[string]any{"autoMode": false, "auto_mode": true}, wantAuto: boolPointer(false)},
		{name: "invalid values ignored", meta: map[string]any{"yoloMode": "true", "autoMode": 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yolo, auto := sessionPermissionModeOverrides(test.meta)
			if !equalOptionalBool(yolo, test.wantYolo) || !equalOptionalBool(auto, test.wantAuto) {
				t.Fatalf("got yolo=%v auto=%v, want yolo=%v auto=%v", yolo, auto, test.wantYolo, test.wantAuto)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func equalOptionalBool(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func encodeACP(t *testing.T, encoder *json.Encoder, value any) {
	t.Helper()
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeACP(t *testing.T, decoder *json.Decoder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func decodeACPNonQueue(t *testing.T, decoder *json.Decoder) map[string]any {
	t.Helper()
	for {
		value := decodeACP(t, decoder)
		if value["method"] != "x.ai/queue/changed" {
			return value
		}
	}
}

func assertPromptComplete(t *testing.T, value map[string]any, sessionID, promptID, stopReason string) {
	t.Helper()
	if value["method"] != "x.ai/session/prompt_complete" {
		t.Fatalf("unexpected prompt completion: %#v", value)
	}
	params := value["params"].(map[string]any)
	if params["sessionId"] != sessionID || params["stopReason"] != stopReason || params["agentResult"] != nil {
		t.Fatalf("unexpected prompt completion: %#v", value)
	}
	if promptID != "" && params["promptId"] != promptID {
		t.Fatalf("unexpected prompt completion: %#v", value)
	}
}
