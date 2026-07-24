package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseClientHunkTrackerMode(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want tools.HunkTrackerMode
	}{
		{`{}`, tools.HunkTrackerOff},
		{`{"clientCapabilities":{"_meta":{"x.ai/hunkTracker":{"mode":"   "}}}}`, tools.HunkTrackerOff},
		{`{"clientCapabilities":{"_meta":{"x.ai/hunkTracker":{"mode":"agent_only"}}}}`, tools.HunkTrackerAgentOnly},
		{`{"clientCapabilities":{"_meta":{"x.ai/hunkTracker":{"mode":" ALL_DIRTY "}}}}`, tools.HunkTrackerAllDirty},
		{`{"clientCapabilities":{"_meta":{"x.ai/hunkTracker":{"mode":"Disabled"}}}}`, tools.HunkTrackerOff},
		{`{"clientCapabilities":{"_meta":{"x.ai/hunkTracker":{"mode":"unknown"}}}}`, tools.HunkTrackerAllDirty},
		{`{`, tools.HunkTrackerOff},
	} {
		if got := parseClientHunkTrackerMode([]byte(test.raw)); got != test.want {
			t.Errorf("parseClientHunkTrackerMode(%q)=%q want %q", test.raw, got, test.want)
		}
	}
}

func TestClientHunkTrackerModeReachesSessionFactory(t *testing.T) {
	root := t.TempDir()
	var captured tools.HunkTrackerMode
	var output bytes.Buffer
	server := &Server{
		SessionDir: t.TempDir(), output: &output, sessions: make(map[string]*session),
		clientHunkMode: tools.HunkTrackerAgentOnly,
		Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			captured = cfg.HunkTrackerMode
			ws, err := workspace.Open(cfg.CWD)
			if err != nil {
				return nil, nil, err
			}
			registry := tools.NewRegistryWithHunkMode(ws, approver, cfg.HunkTrackerMode)
			return &agent.Runner{Tools: registry}, func() { _ = registry.Close() }, nil
		},
	}
	params, err := json.Marshal(map[string]any{"cwd": root, "mcpServers": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	server.handleNewSession(context.Background(), message{ID: json.RawMessage("1"), Params: params})
	if captured != tools.HunkTrackerAgentOnly {
		t.Fatalf("factory mode=%q", captured)
	}
	for _, current := range server.sessions {
		current.close()
	}
}
