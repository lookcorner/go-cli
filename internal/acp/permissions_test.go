package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestServerApproverRemembersExactRequestUntilReset(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, pending: make(map[string]chan permissionResult)}
	approver := &serverApprover{server: server, sessionID: "permission-session"}
	approve := func(action, detail, option string) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- approver.Approve(context.Background(), action, detail) }()
		deadline := time.Now().Add(time.Second)
		var id string
		for id == "" && time.Now().Before(deadline) {
			server.mu.Lock()
			for id = range server.pending {
				break
			}
			server.mu.Unlock()
			if id == "" {
				time.Sleep(time.Millisecond)
			}
		}
		if id == "" {
			t.Fatal("permission request was not registered")
		}
		server.handleClientResponse(message{
			ID:     json.RawMessage(quoteJSON(id)),
			Result: json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"` + option + `"}}`),
		})
		select {
		case err := <-done:
			return err
		case <-time.After(time.Second):
			t.Fatal("permission request did not complete")
			return nil
		}
	}

	if err := approve("shell", "git status", "allow_always"); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	options := messages[0]["params"].(map[string]any)["options"].([]any)
	if len(options) != 3 || options[1].(map[string]any)["optionId"] != "allow_always" {
		t.Fatalf("permission options=%#v", options)
	}
	requests := server.nextRequest.Load()
	if err := approver.Approve(context.Background(), "shell", "git status"); err != nil || server.nextRequest.Load() != requests {
		t.Fatalf("remembered request err=%v requests=%d", err, server.nextRequest.Load())
	}
	if err := approve("shell", "git diff", "allow_once"); err != nil {
		t.Fatal(err)
	}
	approver.reset()
	if err := approve("shell", "git status", "allow_once"); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionResetClearsAllLiveSessionsAndIsFireAndForget(t *testing.T) {
	first := &serverApprover{grants: map[permissionGrant]struct{}{{action: "shell", detail: "one"}: {}}}
	second := &serverApprover{grants: map[permissionGrant]struct{}{{action: "shell", detail: "two"}: {}}}
	closed := &serverApprover{grants: map[permissionGrant]struct{}{{action: "shell", detail: "closed"}: {}}}
	server := &Server{sessions: map[string]*session{
		"first":  {permissions: first},
		"second": {permissions: second},
		"closed": {permissions: closed, closed: true},
	}}
	server.handlePermissionReset()
	if len(first.grants) != 0 || len(second.grants) != 0 {
		t.Fatalf("live grants remain: first=%v second=%v", first.grants, second.grants)
	}
	if len(closed.grants) != 1 {
		t.Fatalf("closed session was mutated: %v", closed.grants)
	}

	server = &Server{SessionDir: t.TempDir(), Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	var output bytes.Buffer
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"x.ai/permissions/reset","params":{}}` + "\n")
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("notification produced a response: %s", output.String())
	}
}

func permissionRegistry(t *testing.T, mode tools.PermissionMode) *tools.Registry {
	return permissionRegistryWithAutoLock(t, mode, false)
}

func permissionRegistryWithAutoLock(t *testing.T, mode tools.PermissionMode, autoLocked bool) *tools.Registry {
	return permissionRegistryWithLocks(t, mode, autoLocked, false)
}

func permissionRegistryWithLocks(t *testing.T, mode tools.PermissionMode, alwaysApproveLocked, autoModeLocked bool) *tools.Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := tools.NewModeApproverWithLocks(mode, tools.PromptApprover{Mode: tools.PermissionAuto}, alwaysApproveLocked, autoModeLocked)
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
	managed := permissionRegistryWithAutoLock(t, tools.PermissionPrompt, true)
	autoDisabled := permissionRegistryWithLocks(t, tools.PermissionPrompt, false, true)
	defer first.Close()
	defer second.Close()
	defer locked.Close()
	defer managed.Close()
	defer autoDisabled.Close()
	server := &Server{sessions: map[string]*session{
		"first":         {runner: &agent.Runner{Tools: first}},
		"second":        {runner: &agent.Runner{Tools: second}},
		"locked":        {runner: &agent.Runner{Tools: locked}},
		"managed":       {runner: &agent.Runner{Tools: managed}},
		"auto-disabled": {runner: &agent.Runner{Tools: autoDisabled}},
	}}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":true,"permission_mode":"always-approve"}`))
	for name, registry := range map[string]*tools.Registry{"first": first, "second": second} {
		if mode, ok := registry.PermissionMode(); !ok || mode != tools.PermissionAlwaysApprove {
			t.Fatalf("%s mode=%q ok=%v", name, mode, ok)
		}
	}
	if mode, _ := locked.PermissionMode(); mode != tools.PermissionDeny {
		t.Fatalf("explicit deny changed to %q", mode)
	}
	if mode, _ := managed.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("managed lock changed to %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":false,"permission_mode":"ask"}`))
	for name, registry := range map[string]*tools.Registry{"first": first, "second": second} {
		if mode, _ := registry.PermissionMode(); mode != tools.PermissionPrompt {
			t.Fatalf("%s mode=%q", name, mode)
		}
	}
	server.handleYoloModeChanged([]byte(`{"auto_mode":true,"permission_mode":"ask"}`))
	for name, registry := range map[string]*tools.Registry{"first": first, "second": second, "managed": managed} {
		if mode, _ := registry.PermissionMode(); mode != tools.PermissionAuto {
			t.Fatalf("%s explicit auto mode=%q", name, mode)
		}
	}
	if mode, _ := autoDisabled.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("disabled auto gate changed permission to %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"auto_mode":false,"permission_mode":"auto"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("explicit auto false did not win: %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"permission_mode":"auto"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionAuto {
		t.Fatalf("permission_mode auto=%q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":true,"auto_mode":true}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionAlwaysApprove {
		t.Fatalf("explicit yolo did not win: %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"auto_mode":true}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionAuto {
		t.Fatalf("auto did not clear yolo: %q", mode)
	}
	if err := second.SetPermissionMode(tools.PermissionAlwaysApprove); err != nil {
		t.Fatal(err)
	}
	server.handleYoloModeChanged([]byte(`{"permission_mode":"default"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("default did not clear auto: %q", mode)
	}
	if mode, _ := second.PermissionMode(); mode != tools.PermissionAlwaysApprove {
		t.Fatalf("clearing auto disturbed always-approve: %q", mode)
	}
	for _, permissionMode := range []string{"ask", "always-approve", "default"} {
		if err := first.SetPermissionMode(tools.PermissionAuto); err != nil {
			t.Fatal(err)
		}
		server.handleYoloModeChanged([]byte(`{"permission_mode":"` + permissionMode + `"}`))
		if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
			t.Fatalf("permission_mode %s did not clear auto: %q", permissionMode, mode)
		}
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":true,"auto_mode":"invalid"}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionAlwaysApprove {
		t.Fatalf("malformed auto poisoned yolo: %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"yolo_mode":"invalid","auto_mode":true}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionAuto {
		t.Fatalf("malformed yolo poisoned auto: %q", mode)
	}
	server.handleYoloModeChanged([]byte(`{"auto_mode":false,"permission_mode":42}`))
	if mode, _ := first.PermissionMode(); mode != tools.PermissionPrompt {
		t.Fatalf("malformed permission mode poisoned auto: %q", mode)
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
			`{"jsonrpc":"2.0","method":"x.ai/yolo_mode_changed","params":{"yolo_mode":true}}` + "\n" +
			`{"jsonrpc":"2.0","method":"x.ai/yolo_mode_changed","params":{"auto_mode":true}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	registry := <-registries
	if mode, ok := registry.PermissionMode(); !ok || mode != tools.PermissionAuto {
		t.Fatalf("mode=%q ok=%v output=%s", mode, ok, output.String())
	}
	messages := decodeACPOutput(t, output.Bytes())
	responses := 0
	rosterUpdates := 0
	for _, item := range messages {
		if item["id"] != nil {
			responses++
		}
		if item["method"] == "x.ai/sessions/changed" {
			rosterUpdates++
		}
	}
	if responses != 1 || rosterUpdates != 1 {
		t.Fatalf("fire-and-forget responses=%d roster=%d messages=%#v", responses, rosterUpdates, messages)
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
