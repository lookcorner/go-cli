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

func permissionRegistry(t *testing.T, mode tools.PermissionMode) *tools.Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := tools.NewModeApprover(mode, tools.PromptApprover{Mode: tools.PermissionAuto})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := tools.NewPolicyApprover(controller, tools.PromptApprover{Mode: tools.PermissionAuto}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewRegistry(ws, policy)
}

func TestYoloModeChangedUpdatesAllMutableSessions(t *testing.T) {
	first := permissionRegistry(t, tools.PermissionPrompt)
	second := permissionRegistry(t, tools.PermissionPrompt)
	locked := permissionRegistry(t, tools.PermissionDeny)
	defer first.Close()
	defer second.Close()
	defer locked.Close()
	server := &Server{sessions: map[string]*session{
		"first":  {runner: &agent.Runner{Tools: first}},
		"second": {runner: &agent.Runner{Tools: second}},
		"locked": {runner: &agent.Runner{Tools: locked}},
	}}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":true,"permission_mode":"always-approve"}`))
	for name, registry := range map[string]*tools.Registry{"first": first, "second": second} {
		if mode, ok := registry.PermissionMode(); !ok || mode != tools.PermissionAuto {
			t.Fatalf("%s mode=%q ok=%v", name, mode, ok)
		}
	}
	if mode, _ := locked.PermissionMode(); mode != tools.PermissionDeny {
		t.Fatalf("explicit deny changed to %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":false,"permission_mode":"ask"}`))
	for name, registry := range map[string]*tools.Registry{"first": first, "second": second} {
		if mode, _ := registry.PermissionMode(); mode != tools.PermissionPrompt {
			t.Fatalf("%s mode=%q", name, mode)
		}
	}
	server.handleYoloModeChanged([]byte(`{"auto_mode":true,"permission_mode":"auto"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("unsupported auto mode changed permission to %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":false,"auto_mode":true,"permission_mode":"auto"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("disabled yolo mode did not keep prompt mode: %q", mode)
	}
}

func TestYoloModeChangedServeRouteIsFireAndForget(t *testing.T) {
	root := t.TempDir()
	registries := make(chan *tools.Registry, 1)
	server := &Server{SessionDir: t.TempDir(), Factory: func(_ context.Context, cfg SessionConfig, _ tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		controller, err := tools.NewModeApprover(tools.PermissionPrompt, tools.PromptApprover{Mode: tools.PermissionAuto})
		if err != nil {
			return nil, nil, err
		}
		policy, err := tools.NewPolicyApprover(controller, tools.PromptApprover{Mode: tools.PermissionAuto}, nil, nil, nil)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, policy)
		registries <- registry
		return &agent.Runner{Tools: registry}, func() { _ = registry.Close() }, nil
	}}
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":` + quoteJSON(root) + `,"mcpServers":[]}}` + "\n" +
			`{"jsonrpc":"2.0","method":"x.ai/yolo_mode_changed","params":{"yolo_mode":true}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	registry := <-registries
	if mode, ok := registry.PermissionMode(); !ok || mode != tools.PermissionAuto {
		t.Fatalf("mode=%q ok=%v output=%s", mode, ok, output.String())
	}
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 1 || messages[0]["id"] != float64(1) {
		t.Fatalf("notification produced a response: %#v", messages)
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
