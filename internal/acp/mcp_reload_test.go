package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestMCPReloadAllFansOutAcrossLiveSessions(t *testing.T) {
	var output bytes.Buffer
	var calls []string
	reloader := func(name string, err error) *agent.Runner {
		return &agent.Runner{ReloadMCPBase: func(context.Context) error {
			calls = append(calls, name)
			return err
		}}
	}
	server := &Server{output: &output, sessions: map[string]*session{
		"nil":         nil,
		"one":         {runner: reloader("one", errors.New("reload failed"))},
		"two":         {runner: reloader("two", nil)},
		"closed":      {closed: true, runner: reloader("closed", nil)},
		"unsupported": {runner: &agent.Runner{}},
	}}

	server.handleMCPReload(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/internal/reload_all_mcp_servers"})

	if len(calls) != 2 {
		t.Fatalf("reload calls=%v", calls)
	}
	assertMCPReloadResponse(t, &output, 2)
}

func TestMCPReloadProjectMatchesPathComponents(t *testing.T) {
	var output bytes.Buffer
	root := filepath.Join(t.TempDir(), "repo")
	var calls []string
	reloader := func(name string) *agent.Runner {
		return &agent.Runner{ReloadMCPBase: func(context.Context) error {
			calls = append(calls, name)
			return nil
		}}
	}
	server := &Server{output: &output, sessions: map[string]*session{
		"exact":      {cwd: root, runner: reloader("exact")},
		"descendant": {cwd: filepath.Join(root, "subdir"), runner: reloader("descendant")},
		"collision":  {cwd: root + "-test", runner: reloader("collision")},
		"parent":     {cwd: filepath.Dir(root), runner: reloader("parent")},
	}}
	params, _ := json.Marshal(map[string]any{"cwd": root})

	server.handleMCPReload(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/internal/reload_project_mcp_servers", Params: params})

	if len(calls) != 2 {
		t.Fatalf("reload calls=%v", calls)
	}
	assertMCPReloadResponse(t, &output, 2)
}

func TestMCPReloadProjectValidatesCWD(t *testing.T) {
	for _, params := range []string{"", `{}`, `{"cwd":""}`, `{"cwd":"relative"}`, `{"cwd":42}`} {
		t.Run(params, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{}}
			server.handleMCPReload(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/internal/reload_project_mcp_servers", Params: json.RawMessage(params)})
			messages := decodeACPBytes(t, output.Bytes())
			if len(messages) != 1 || messages[0]["error"].(map[string]any)["code"] != float64(-32602) {
				t.Fatalf("response=%#v", messages)
			}
		})
	}
}

func TestMCPReloadRoutesThroughServe(t *testing.T) {
	root := t.TempDir()
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/internal/reload_all_mcp_servers"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/internal/reload_project_mcp_servers","params":{"cwd":` + quoteJSON(root) + `}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{SessionDir: t.TempDir(), Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPBytes(t, output.Bytes())
	if len(messages) != 2 {
		t.Fatalf("responses=%#v", messages)
	}
	for _, response := range messages {
		if response["result"].(map[string]any)["updated"] != float64(0) {
			t.Fatalf("response=%#v", response)
		}
	}
}

func assertMCPReloadResponse(t *testing.T, output *bytes.Buffer, updated float64) {
	t.Helper()
	messages := decodeACPBytes(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["updated"] != updated {
		t.Fatalf("response=%#v", messages)
	}
}
