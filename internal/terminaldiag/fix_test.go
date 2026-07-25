package terminaldiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFixID(t *testing.T) {
	for _, value := range []string{"ssh-wrap", "terminal.ssh-wrap"} {
		id, err := ResolveFixID(value)
		if err != nil || id != SSHWrapID {
			t.Fatalf("value=%q id=%q err=%v", value, id, err)
		}
	}
	if _, err := ResolveFixID("tmux-clipboard"); err == nil {
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
	local := ListAutomaticFixes(FixEnv{Home: "/tmp/home", Shell: "/bin/bash", GOOS: "linux"})
	if len(local) != 1 || local[0].Availability != "here" {
		t.Fatalf("local=%#v", local)
	}
	remote := ListAutomaticFixes(FixEnv{Home: "/tmp/home", Shell: "/bin/bash", GOOS: "linux", SSH: true})
	if remote[0].Availability != "run_locally" {
		t.Fatalf("remote=%#v", remote)
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
