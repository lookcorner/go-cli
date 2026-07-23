package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestJujutsuGitRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed jj fixture is Unix-only")
	}
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	if err := os.Mkdir(filepath.Join(root, ".jj"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$JJ_LOG"
case "$*" in
  "--ignore-working-copy workspace root") printf '%s\n' "$JJ_ROOT" ;;
  "--ignore-working-copy log --no-graph -r @ -T commit_id.shortest(12)") printf 'abcdef123456\n' ;;
  "--ignore-working-copy log --no-graph -r @ -T commit_id") printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' ;;
  "--ignore-working-copy log --no-graph -r @- -T bookmarks.join(\", \")") printf 'main\n' ;;
  "--ignore-working-copy diff --summary") printf 'M changed.txt\n' ;;
  "--ignore-working-copy bookmark list --all -T name ++ if(remote, \"@\" ++ remote, \"\") ++ \"\\n\"") printf 'main\nmain@origin\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "jj"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("JJ_ROOT", root)
	t.Setenv("JJ_LOG", filepath.Join(root, "jj.log"))

	var output bytes.Buffer
	server := &Server{output: &output}
	request := func(id int, method, fields string) map[string]any {
		t.Helper()
		output.Reset()
		params := `{"gitRoot":` + strconv.Quote(root)
		if fields != "" {
			params += "," + fields
		}
		server.handleGit(context.Background(), message{ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: json.RawMessage(params + "}")})
		return decodeACPOutput(t, output.Bytes())[0]
	}

	statusEnvelope := request(1, "x.ai/git/status", "")
	statusResult := statusEnvelope["result"].(map[string]any)
	status := statusResult["result"].(map[string]any)
	if statusResult["error"] != nil || status["root"] != root || status["commit"] != "abcdef123456" || status["branch"] != "main" || len(status["staged"].([]any)) != 0 || status["unstaged"].([]any)[0].(map[string]any)["path"] != "changed.txt" {
		t.Fatalf("status=%#v", statusEnvelope)
	}

	stageEnvelope := request(2, "x.ai/git/stage", `"paths":["changed.txt"]`)
	stage := stageEnvelope["result"].(map[string]any)["result"].(map[string]any)
	if len(stage["paths"].([]any)) != 0 {
		t.Fatalf("stage=%#v", stageEnvelope)
	}

	info := request(3, "x.ai/git/info", "")["result"].(map[string]any)["result"].(map[string]any)
	if info["root"] != root || info["currentBranch"] != "main" || info["vcsKind"] != "jujutsuColocated" {
		t.Fatalf("info=%#v", info)
	}
	if commit := request(4, "x.ai/git/current_commit", "")["result"].(map[string]any)["result"]; commit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("commit=%#v", commit)
	}
	branches := request(5, "x.ai/git/branches", "")["result"].(map[string]any)["result"].(map[string]any)
	if branches["currentBranch"] != "main" || branches["repoRoot"] != root || len(branches["branches"].([]any)) != 2 {
		t.Fatalf("branches=%#v", branches)
	}
	committed := request(6, "x.ai/git/commit", `"message":"ship it"`)["result"].(map[string]any)["result"].(map[string]any)
	if committed["commitHash"] != "abcdef123456" || committed["output"] != "Commit described and new change started" {
		t.Fatalf("committed=%#v", committed)
	}
	for id, method := range map[int]string{7: "x.ai/git/stage/content", 8: "x.ai/git/unstage", 9: "x.ai/git/discard"} {
		fields := ""
		if method == "x.ai/git/discard" {
			fields = `"paths":["changed.txt"]`
		}
		result := request(id, method, fields)["result"].(map[string]any)
		if result["error"] != nil || len(result["result"].(map[string]any)) != 0 {
			t.Fatalf("%s=%#v", method, result)
		}
	}

	for id, method := range map[int]string{10: "x.ai/git/checkout", 11: "x.ai/git/stash"} {
		envelope := request(id, method, "")
		if code := envelope["error"].(map[string]any)["code"]; code != float64(-32602) {
			t.Fatalf("%s=%#v", method, envelope)
		}
	}
	log, err := os.ReadFile(filepath.Join(root, "jj.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"describe -m ship it", "new", "restore changed.txt"} {
		if !bytes.Contains(log, []byte(command+"\n")) {
			t.Fatalf("missing command %q in %q", command, log)
		}
	}
}
