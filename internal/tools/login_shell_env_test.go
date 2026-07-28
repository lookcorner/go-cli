package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestCaptureLoginShellEnvBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix login shell capture")
	}
	entries := CaptureLoginShellEnv(context.Background())
	// Best-effort: empty is OK on restricted hosts; when present, PATH should exist.
	for _, entry := range entries {
		if strings.HasPrefix(entry, "PATH=") && len(entry) > 5 {
			return
		}
	}
	if len(entries) != 0 {
		t.Fatalf("captured env without PATH: %#v", entries[:min(3, len(entries))])
	}
}
