package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestCompletionsGenerateSupportedShells(t *testing.T) {
	for _, shell := range []string{"bash", "elvish", "fish", "powershell", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := runCompletions([]string{shell}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if script := stdout.String(); !strings.Contains(script, "gork __complete") {
				t.Fatalf("%s completion does not use the shared query:\n%s", shell, script)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestCompletionCandidatesFollowCommandTree(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{nil, []string{"--always-approve", "--cwd", "--max-turns", "--reasoning-effort", "--single", "-m", "-p", "-V", "completions", "dashboard", "models", "plugin", "share", "trace", "version", "wrap", "worktree"}},
		{[]string{"dashboard", "--"}, []string{"--config", "--fullscreen", "--minimal", "--session-dir", "--trust", "--workspace"}},
		{[]string{"completions", ""}, []string{"bash", "elvish", "fish", "powershell", "zsh"}},
		{[]string{"plugin", ""}, []string{"install", "list", "marketplace", "uninstall", "update"}},
		{[]string{"mcp", ""}, []string{"add", "doctor", "list", "remove"}},
		{[]string{"mcp", "add", "--t"}, []string{"--transport"}},
		{[]string{"plugin", "marketplace", ""}, []string{"add", "list", "remove", "update"}},
		{[]string{"sessions", "search", "query", "--l"}, []string{"--limit"}},
		{[]string{"worktree", "db", ""}, []string{"path", "rebuild", "stats"}},
		{[]string{"worktree", "p"}, []string{"prune"}},
	}
	for _, test := range tests {
		choices := strings.Join(completionCandidates(test.args), " ")
		for _, value := range test.want {
			if !strings.Contains(" "+choices+" ", " "+value+" ") {
				t.Fatalf("arguments %v missing %q in %q", test.args, value, choices)
			}
		}
	}
}

func TestCompletionCandidatesSkipFlagValues(t *testing.T) {
	if choices := completionCandidates([]string{"models", "--config", "custom.toml"}); len(choices) != 0 {
		t.Fatalf("config value received command completions: %v", choices)
	}
	choices := completionCandidates([]string{"sessions", "search", "query", "--limit", "20", "--"})
	if len(choices) != 1 || choices[0] != "--limit" {
		t.Fatalf("flags after a consumed value=%v", choices)
	}
}

func TestCompletionQueryWritesOneCandidatePerLine(t *testing.T) {
	var output bytes.Buffer
	if err := runCompletionQuery([]string{"mo"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "models\n" {
		t.Fatalf("query output=%q", output.String())
	}
}

func TestCompletionQueryDispatchesBeforeConfiguration(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"__complete", "worktree", "d"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "db\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestBashAndZshCompletionsParse(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		var script bytes.Buffer
		if err := runCompletions([]string{shell}, &script, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(path, "-n")
		command.Stdin = strings.NewReader(script.String())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s completion syntax: %v\n%s\n%s", shell, err, output, script.String())
		}
	}
}

func TestCompletionsRejectInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"bash", "zsh"}, {"tcsh"}} {
		var stdout, stderr bytes.Buffer
		if err := runCompletions(args, &stdout, &stderr); err == nil {
			t.Fatalf("arguments %v were accepted", args)
		}
		if !strings.Contains(stderr.String(), "Usage: gork completions") {
			t.Fatalf("usage missing for %v: %s", args, stderr.String())
		}
	}
}
