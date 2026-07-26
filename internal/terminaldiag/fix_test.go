package terminaldiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFixID(t *testing.T) {
	cases := map[string]string{
		"ssh-wrap": "terminal.ssh-wrap", "terminal.ssh-wrap": SSHWrapID,
		"tmux-clipboard": TmuxClipboardID, "terminal.tmux-clipboard": TmuxClipboardID,
		"dcs-passthrough": DCSPassthroughID, "tmux-extended-keys": TmuxExtendedKeysID,
		"tmux-truecolor": TmuxTruecolorID, "terminal.tmux-truecolor": TmuxTruecolorID,
	}
	for value, want := range cases {
		id, err := ResolveFixID(value)
		if err != nil || id != want {
			t.Fatalf("value=%q id=%q err=%v want=%q", value, id, err, want)
		}
	}
	if _, err := ResolveFixID("not-a-fix"); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestPlanAndApplySSHWrapBash(t *testing.T) {
	home := t.TempDir()
	env := FixEnv{Home: home, Shell: "/bin/bash", GOOS: "linux"}
	plan, err := PlanSSHWrap(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != filepath.Join(home, ".bashrc") || plan.Alias != "alias ssh='gork wrap ssh'" {
		t.Fatalf("plan=%#v", plan)
	}
	outcome, err := ApplySSHWrap(plan)
	if err != nil || outcome.Status != FixApplied {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	data, err := os.ReadFile(plan.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# >>> gork doctor >>>", "# >>> terminal.ssh-wrap >>>", "alias ssh='gork wrap ssh'", "# <<< terminal.ssh-wrap <<<", "# <<< gork doctor <<<"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	outcome, err = ApplySSHWrap(plan)
	if err != nil || outcome.Status != FixAlreadyConfigured {
		t.Fatalf("second apply=%#v err=%v", outcome, err)
	}
}

func TestPlanSSHWrapRejectsConflictsAndRemote(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(path, []byte("alias ssh='ssh -A'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSSHWrap(FixEnv{Home: home, Shell: "/bin/zsh", GOOS: "darwin"}); err == nil || !strings.Contains(err.Error(), "existing") {
		t.Fatalf("conflict err=%v", err)
	}
	if _, err := PlanSSHWrap(FixEnv{Home: home, Shell: "/bin/zsh", GOOS: "darwin", SSH: true}); err == nil || !strings.Contains(err.Error(), "local computer") {
		t.Fatalf("remote err=%v", err)
	}
	if _, err := PlanSSHWrap(FixEnv{Home: home, Shell: "/bin/zsh", GOOS: "windows"}); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("windows err=%v", err)
	}
}

func TestListAutomaticFixesAvailability(t *testing.T) {
	local := ListAutomaticFixes(FixEnv{Home: "/tmp/home", Shell: "/bin/bash", GOOS: "linux", Tmux: true})
	if len(local) != 5 || local[0].Availability != "here" || local[1].Availability != "here" || local[4].Handle != TmuxTruecolorHandle {
		t.Fatalf("local=%#v", local)
	}
	remote := ListAutomaticFixes(FixEnv{Home: "/tmp/home", Shell: "/bin/bash", GOOS: "linux", SSH: true})
	if remote[0].Availability != "run_locally" {
		t.Fatalf("remote=%#v", remote)
	}
	outside := ListAutomaticFixes(FixEnv{Home: "/tmp/home", Shell: "/bin/bash", GOOS: "linux", Tmux: false})
	if outside[1].Availability != "unsupported" {
		t.Fatalf("outside tmux=%#v", outside)
	}
}

func TestDetectPOSIXSSHCustomization(t *testing.T) {
	if detectPOSIXSSHCustomization("alias ssh='ssh -A'\n") == "" {
		t.Fatal("alias missed")
	}
	if detectPOSIXSSHCustomization("ssh() { :; }\n") == "" {
		t.Fatal("function missed")
	}
	if detectPOSIXSSHCustomization("alias ssh_wrap='ssh'\n") != "" {
		t.Fatal("unrelated alias treated as conflict")
	}
}

func TestPlanAndApplyTmuxClipboard(t *testing.T) {
	home := t.TempDir()
	env := FixEnv{Home: home, Tmux: true}
	plan, err := PlanTmuxOption(env, TmuxClipboardID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != filepath.Join(home, ".tmux.conf") || plan.Alias != "set -g set-clipboard on" {
		t.Fatalf("plan=%#v", plan)
	}
	outcome, err := ApplyTmuxOption(plan)
	if err != nil || outcome.Status != FixApplied {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	data, err := os.ReadFile(plan.Path)
	if err != nil || !strings.Contains(string(data), "set -g set-clipboard on") {
		t.Fatalf("tmux.conf=%q err=%v", data, err)
	}
	outcome, err = ApplyTmuxOption(plan)
	if err != nil || outcome.Status != FixAlreadyConfigured {
		t.Fatalf("second=%#v err=%v", outcome, err)
	}
}

func TestPlanTmuxPreservesSiblingManagedItems(t *testing.T) {
	home := t.TempDir()
	env := FixEnv{Home: home, Tmux: true}
	first, err := PlanTmuxOption(env, TmuxClipboardID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTmuxOption(first); err != nil {
		t.Fatal(err)
	}
	second, err := PlanTmuxOption(env, DCSPassthroughID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTmuxOption(second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"terminal.tmux-clipboard", "set -g set-clipboard on", "terminal.dcs-passthrough", "set -wg allow-passthrough on"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestPlanTmuxRejectsConflictAndOutsideSession(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(path, []byte("set -g set-clipboard off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanTmuxOption(FixEnv{Home: home, Tmux: true}, TmuxClipboardID); err == nil || !strings.Contains(err.Error(), "existing") {
		t.Fatalf("conflict err=%v", err)
	}
	if _, err := PlanTmuxOption(FixEnv{Home: home, Tmux: false}, TmuxClipboardID); err == nil || !strings.Contains(err.Error(), "tmux session") {
		t.Fatalf("outside err=%v", err)
	}
}

func TestPlanTmuxHealthyDirectAssignmentSkipsWrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(path, []byte("set -g extended-keys on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanTmuxOption(FixEnv{Home: home, Tmux: true}, TmuxExtendedKeysID)
	if err != nil || !plan.SkipWrite {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	outcome, err := ApplyTmuxOption(plan)
	if err != nil || outcome.Status != FixAlreadyConfigured {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "gork doctor") {
		t.Fatalf("unexpected write: %q err=%v", data, err)
	}
}

func TestPlanAndApplyTmuxTruecolor(t *testing.T) {
	home := t.TempDir()
	env := FixEnv{Home: home, Tmux: true}
	plan, err := PlanTmuxTruecolor(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != filepath.Join(home, ".tmux.conf") || !strings.Contains(plan.Alias, "tmux-256color") || !strings.Contains(plan.Alias, "terminal-features") {
		t.Fatalf("plan=%#v", plan)
	}
	outcome, err := ApplyTmuxTruecolor(plan)
	if err != nil || outcome.Status != FixApplied {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	data, err := os.ReadFile(plan.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"terminal.tmux-truecolor", `set -g default-terminal "tmux-256color"`, `set -as terminal-features ",*:RGB"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	outcome, err = ApplyTmuxTruecolor(plan)
	if err != nil || outcome.Status != FixAlreadyConfigured {
		t.Fatalf("second=%#v err=%v", outcome, err)
	}
}

func TestPlanTmuxTruecolorRejectsConflict(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(path, []byte("set -g default-terminal \"xterm\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanTmuxTruecolor(FixEnv{Home: home, Tmux: true}); err == nil || !strings.Contains(err.Error(), "existing") {
		t.Fatalf("conflict err=%v", err)
	}
}
