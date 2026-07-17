package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestBackgroundCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	id, err := manager.Start(context.Background(), "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := manager.Output(id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "exited successfully") {
			if !strings.Contains(output, "hello") {
				t.Fatalf("missing command output: %s", output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not complete: %s", output)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundCommandKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	id, err := manager.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	output, err := manager.Output(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "status: running") {
		t.Fatalf("process still running: %s", output)
	}
}
