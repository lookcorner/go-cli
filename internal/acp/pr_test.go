package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestLookupPRStatus(t *testing.T) {
	root := t.TempDir()
	installFakeGH(t, root, `
case "$1" in
  pr) printf '%s' '{"state":"OPEN","url":"https://example.test/pr/42","isDraft":false,"number":42,"title":"Ready"}' ;;
  api) printf '\033[1;32m%s\033[0m' '{"data":{"resource":{"isInMergeQueue":true}}}' ;;
esac
`)
	status := lookupPRStatus(context.Background(), root, "feature")
	if status == nil || status.URL != "https://example.test/pr/42" || status.State != "open" || !status.IsInMergeQueue || status.Number == nil || *status.Number != 42 || status.Title == nil || *status.Title != "Ready" {
		t.Fatalf("unexpected PR status: %#v", status)
	}
}

func TestLookupPRStatusStatesAndFailures(t *testing.T) {
	tests := []struct {
		name, output, want string
	}{
		{name: "draft", output: `{"state":"OPEN","url":"https://example.test/draft","isDraft":true}`, want: "draft"},
		{name: "merged", output: `{"state":"Merged","url":"https://example.test/merged"}`, want: "merged"},
		{name: "closed", output: `{"state":"CLOSED","url":"https://example.test/closed"}`, want: "closed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			installFakeGH(t, root, "printf '%s' '"+test.output+"'")
			status := lookupPRStatus(context.Background(), root, "feature")
			if status == nil || status.State != test.want || status.IsInMergeQueue {
				t.Fatalf("unexpected PR status: %#v", status)
			}
		})
	}
	for _, script := range []string{"exit 1", "printf 'not-json'", `printf '{"state":"OPEN"}'`} {
		root := t.TempDir()
		installFakeGH(t, root, script)
		if status := lookupPRStatus(context.Background(), root, "missing"); status != nil {
			t.Fatalf("failed gh lookup returned %#v", status)
		}
	}
}

func TestLookupMergeQueueDegradesToFalse(t *testing.T) {
	for _, output := range []string{
		`{"data":{"resource":{"isInMergeQueue":false}}}`,
		`{"data":{"resource":null}}`,
		`not-json`,
	} {
		root := t.TempDir()
		installFakeGH(t, root, "printf '%s' '"+output+"'")
		if lookupMergeQueue(context.Background(), root, "https://example.test/pr/1") {
			t.Fatalf("merge queue lookup accepted %q", output)
		}
	}
	root := t.TempDir()
	installFakeGH(t, root, "exit 1")
	if lookupMergeQueue(context.Background(), root, "https://example.test/pr/1") {
		t.Fatal("failed merge queue command returned true")
	}
}

func TestPRStatusServeRoute(t *testing.T) {
	root := t.TempDir()
	installFakeGH(t, root, "exit 1")
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		t.Fatal("PR status route started a session")
		return nil, nil, nil
	}}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x.ai/pr/status","params":{"cwd":` + quoteJSON(root) + `,"branch":"main"}}` + "\n")
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)
	if result["pr"] != nil || len(result["updatedSessionIds"].([]any)) != 0 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestPRStatusRejectsMalformedParameters(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.handlePRStatus(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage("{")})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestStripANSI(t *testing.T) {
	input := []byte("\x1b[38;5;208m{\"ok\":true}\x1b[0m\n")
	if got := string(stripANSI(input)); got != "{\"ok\":true}\n" {
		t.Fatalf("stripANSI=%q", got)
	}
}

func installFakeGH(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
}
