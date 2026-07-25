package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorCLIHumanAndJSON(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM_PROGRAM", "TestTerm")
	t.Setenv("TMUX", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("NO_COLOR", "")

	var human, stderr bytes.Buffer
	if err := runDoctor(nil, &human, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment", "terminal     TestTerm", "Clipboard routes"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human missing %q:\n%s", want, human.String())
		}
	}

	var jsonOutput bytes.Buffer
	if err := runDoctor([]string{"--json"}, &jsonOutput, &stderr); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["schemaVersion"] != "1" {
		t.Fatalf("schema=%#v", report["schemaVersion"])
	}
}

func TestDoctorCLIRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runDoctor([]string{"extra"}, &stdout, &stderr); err == nil {
		t.Fatal("doctor accepted a positional argument")
	}
}

func TestDoctorEarlyDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"schemaVersion"`) {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestDoctorFixListAndApplySSHWrap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("VSCODE_INJECTION", "")
	t.Setenv("TERM_PROGRAM", "TestTerm")
	t.Setenv("TMUX", "")

	var list, stderr bytes.Buffer
	if err := runDoctor([]string{"fix"}, &list, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), "ssh-wrap") || !strings.Contains(list.String(), "gork doctor fix ssh-wrap --yes") {
		t.Fatalf("list=%s", list.String())
	}
	if !strings.Contains(list.String(), "tmux-clipboard") || !strings.Contains(list.String(), "unavailable") {
		t.Fatalf("tmux list=%s", list.String())
	}

	var preview bytes.Buffer
	if err := runDoctor([]string{"fix", "ssh-wrap"}, &preview, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.String(), "alias ssh='gork wrap ssh'") || !strings.Contains(preview.String(), "Re-run with --yes") {
		t.Fatalf("preview=%s", preview.String())
	}

	var applied bytes.Buffer
	if err := runOnce([]string{"doctor", "fix", "terminal.ssh-wrap", "--yes"}, strings.NewReader(""), &applied, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(applied.String(), "Set up SSH wrapping") {
		t.Fatalf("applied=%s stderr=%s", applied.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil || !strings.Contains(string(data), "alias ssh='gork wrap ssh'") {
		t.Fatalf("bashrc=%q err=%v", data, err)
	}

	var again bytes.Buffer
	if err := runDoctor([]string{"fix", "ssh-wrap", "--yes"}, &again, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.String(), "already configured") {
		t.Fatalf("again=%s", again.String())
	}
}

func TestDoctorFixRejectsUnknownAndRemote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	var stdout, stderr bytes.Buffer
	if err := runDoctor([]string{"fix", "not-a-fix"}, &stdout, &stderr); err == nil {
		t.Fatal("unknown fix accepted")
	}
	t.Setenv("SSH_CONNECTION", "1 2 3 4")
	if err := runDoctor([]string{"fix", "ssh-wrap", "--yes"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "local computer") {
		t.Fatalf("remote err=%v", err)
	}
}

func TestDoctorFixApplyTmuxClipboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	t.Setenv("BYOBU_CONFIG_DIR", "")
	var stdout, stderr bytes.Buffer
	if err := runDoctor([]string{"fix", "tmux-clipboard", "--yes"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "set -g set-clipboard on") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(home, ".tmux.conf"))
	if err != nil || !strings.Contains(string(data), "set -g set-clipboard on") {
		t.Fatalf("tmux.conf=%q err=%v", data, err)
	}
	var keys bytes.Buffer
	if err := runDoctor([]string{"fix", "terminal.tmux-extended-keys", "--yes"}, &keys, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(home, ".tmux.conf"))
	if err != nil || !strings.Contains(string(data), "extended-keys") || !strings.Contains(string(data), "set-clipboard") {
		t.Fatalf("siblings=%q err=%v", data, err)
	}
}
