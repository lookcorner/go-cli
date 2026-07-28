package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CaptureLoginShellEnv runs a login shell once and returns KEY=value pairs from
// `env -0` after sourcing the user rc file. Failures return nil (caller keeps
// the current environment).
func CaptureLoginShellEnv(ctx context.Context) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/bash"
	}
	rc := ".bashrc"
	base := strings.ToLower(filepath.Base(shell))
	switch {
	case strings.Contains(base, "zsh"):
		rc = ".zshrc"
	case strings.Contains(base, "fish"):
		return nil
	}
	script := "source \"$HOME/" + rc + "\" 2>/dev/null; command env -0 2>/dev/null"
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, shell, "-lc", script)
	cmd.Env = os.Environ()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil
	}
	raw := stdout.Bytes()
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		entry := string(part)
		if !strings.Contains(entry, "=") {
			continue
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
