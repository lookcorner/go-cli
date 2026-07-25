package terminaldiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleCommandReportAndAliases(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "TestTerm")
	t.Setenv("TMUX", "")
	for _, prompt := range []string{"/doctor", "/terminal-setup", "/terminal-check ignored", "/terminal-info"} {
		message, ok := HandleCommand(prompt)
		if !ok || !strings.Contains(message, "Environment\n") {
			t.Fatalf("prompt=%q ok=%v message=%q", prompt, ok, message)
		}
	}
	if _, ok := HandleCommand("/help"); ok {
		t.Fatal("non-doctor command accepted")
	}
}

func TestHandleCommandFixListPreviewAndApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("TMUX", "")
	t.Setenv("VSCODE_INJECTION", "")
	t.Setenv("TERM_PROGRAM", "TestTerm")

	list, ok := HandleCommand("/doctor fix")
	if !ok || !strings.Contains(list, "ssh-wrap") {
		t.Fatalf("list=%q ok=%v", list, ok)
	}
	preview, ok := HandleCommand("/doctor fix ssh-wrap")
	if !ok || !strings.Contains(preview, "alias ssh='gork wrap ssh'") || !strings.Contains(preview, "`/doctor fix ssh-wrap --yes`") {
		t.Fatalf("preview=%q ok=%v", preview, ok)
	}
	applied, ok := HandleCommand("/doctor fix terminal.ssh-wrap --yes")
	if !ok || !strings.Contains(applied, "Set up SSH wrapping") {
		t.Fatalf("applied=%q ok=%v", applied, ok)
	}
	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil || !strings.Contains(string(data), "alias ssh='gork wrap ssh'") {
		t.Fatalf("bashrc=%q err=%v", data, err)
	}
	if message, ok := HandleCommand("/doctor fix not-a-fix"); !ok || !strings.Contains(message, SlashUsage) {
		t.Fatalf("unknown=%q ok=%v", message, ok)
	}
}

func TestSuggestFixArgs(t *testing.T) {
	if SuggestFixArgs("") != nil {
		t.Fatal("empty query should stay closed")
	}
	if got := SuggestFixArgs("f"); len(got) != 1 || got[0] != "fix" {
		t.Fatalf("got=%v", got)
	}
	got := SuggestFixArgs("fix tmux")
	if len(got) == 0 || got[0] != "fix tmux-clipboard" {
		t.Fatalf("got=%v", got)
	}
	if SuggestFixArgs("fix ssh-wrap") != nil {
		t.Fatal("complete id should close suggestions")
	}
}
