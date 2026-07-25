//go:build linux

package clipboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPrimaryPlatformUsesPrimarySelection(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PRIMARY_LOG\"\n[ \"$*\" = '-o -selection primary' ] && printf primary\n"
	path := filepath.Join(bin, "xclip")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISPLAY", ":99")
	t.Setenv("PRIMARY_LOG", logPath)
	t.Setenv("PATH", bin)

	text, err := readPrimaryPlatform(context.Background())
	if err != nil || text != "primary" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "-o -selection primary" || strings.Contains(got, "clipboard") {
		t.Fatalf("args=%q", got)
	}
}

func TestReadPrimaryPlatformRequiresDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	if _, err := readPrimaryPlatform(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err=%v", err)
	}
}
