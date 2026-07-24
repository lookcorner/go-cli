package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func TestExportCLIWritesStdoutFileAndClipboard(t *testing.T) {
	dir := t.TempDir()
	writeCLISession(t, dir, "export-one", t.TempDir(), "Inspect auth", "Auth is ready.")

	var stdout, stderr bytes.Buffer
	copied := ""
	copyText := func(value string) error {
		copied = value
		return nil
	}
	if err := runExportCommand(dir, []string{"export-one"}, &stdout, &stderr, copyText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "## User\n\nInspect auth") || !strings.Contains(stdout.String(), "## Assistant\n\nAuth is ready.") || copied != "" {
		t.Fatalf("stdout=%q copied=%q", stdout.String(), copied)
	}

	root := t.TempDir()
	target := filepath.Join(root, "nested", "conversation.md")
	stdout.Reset()
	stderr.Reset()
	if err := runExportCommand(dir, []string{"export-one", target}, &stdout, &stderr, copyText); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "Inspect auth") || stdout.Len() != 0 || !strings.Contains(stderr.String(), target) {
		t.Fatalf("data=%q stdout=%q stderr=%q err=%v", data, stdout.String(), stderr.String(), err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}

	stderr.Reset()
	if err := runExportCommand(dir, []string{"--clipboard", "export-one"}, &stdout, &stderr, copyText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(copied, "Auth is ready.") || !strings.Contains(stderr.String(), "copied to clipboard") {
		t.Fatalf("copied=%q stderr=%q", copied, stderr.String())
	}
}

func TestExportCLIArgumentsMissingSessionsAndClipboardFailure(t *testing.T) {
	dir := t.TempDir()
	writeCLISession(t, dir, "export-one", t.TempDir(), "Prompt", "Response")
	logger, err := sessionlog.NewLoggerWithID(dir, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("pending", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		nil,
		{"one", "two", "three"},
		{"--bad", "export-one"},
		{"../escape"},
		{"missing"},
		{"pending"},
	} {
		stdout.Reset()
		stderr.Reset()
		if err := runExportCommand(dir, args, &stdout, &stderr, func(string) error { return nil }); err == nil {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
	copyErr := errors.New("clipboard unavailable")
	if err := runExportCommand(dir, []string{"export-one", "-c"}, &stdout, &stderr, func(string) error { return copyErr }); !errors.Is(err, copyErr) {
		t.Fatalf("clipboard error=%v", err)
	}
}

func TestExportPathExpandsHomeAndRelativePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := exportPath("~/exports/conversation.md")
	if err != nil || path != filepath.Join(home, "exports", "conversation.md") {
		t.Fatalf("home path=%q err=%v", path, err)
	}
	path, err = exportPath("~")
	if err != nil || path != home {
		t.Fatalf("bare home path=%q err=%v", path, err)
	}
	path, err = exportPath("conversation.md")
	if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "conversation.md" {
		t.Fatalf("relative path=%q err=%v", path, err)
	}
}
