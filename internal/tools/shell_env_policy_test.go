package tools

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestApplyShellEnvironmentPolicyNoop(t *testing.T) {
	env := []string{"PATH=/bin", "MY_API_KEY=x"}
	out := ApplyShellEnvironmentPolicy(env, DefaultShellEnvironmentPolicy())
	if !slices.Equal(out, env) {
		t.Fatalf("noop mutated env: %#v", out)
	}
}

func TestApplyShellEnvironmentPolicyDefaultExcludes(t *testing.T) {
	policy := DefaultShellEnvironmentPolicy()
	policy.IgnoreDefaultExcludes = false
	out := ApplyShellEnvironmentPolicy([]string{
		"PATH=/bin",
		"MY_API_KEY=x",
		"MY_SECRET=y",
		"GH_TOKEN=z",
	}, policy)
	got := mapFromEnv(out)
	if got["PATH"] != "/bin" || got["MY_API_KEY"] != "" || got["MY_SECRET"] != "" || got["GH_TOKEN"] != "" {
		t.Fatalf("got=%#v", got)
	}
}

func TestApplyShellEnvironmentPolicyInheritNoneSet(t *testing.T) {
	policy := ShellEnvironmentPolicy{
		Inherit:               ShellEnvironmentInheritNone,
		IgnoreDefaultExcludes: true,
		Set:                   map[string]string{"PATH": "/usr/bin", "MY_FLAG": "1"},
	}
	out := ApplyShellEnvironmentPolicy([]string{"PATH=/bin", "HOME=/home/me"}, policy)
	got := mapFromEnv(out)
	if got["PATH"] != "/usr/bin" || got["MY_FLAG"] != "1" || got["HOME"] != "" {
		t.Fatalf("got=%#v", got)
	}
}

func TestApplyShellEnvironmentPolicyInheritCoreAndExclude(t *testing.T) {
	policy := ShellEnvironmentPolicy{
		Inherit:               ShellEnvironmentInheritCore,
		IgnoreDefaultExcludes: true,
		Exclude:               []string{"LANG"},
		IncludeOnly:           []string{"PATH", "HOME"},
	}
	out := ApplyShellEnvironmentPolicy([]string{
		"PATH=/bin",
		"HOME=/home",
		"LANG=C",
		"SECRET_STUFF=1",
	}, policy)
	got := mapFromEnv(out)
	if got["PATH"] != "/bin" || got["HOME"] != "/home" || got["LANG"] != "" || got["SECRET_STUFF"] != "" {
		t.Fatalf("got=%#v", got)
	}
}

func TestApplyShellEnvironmentPolicyWindowsPathext(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only PATHEXT seed")
	}
	policy := ShellEnvironmentPolicy{
		Inherit:               ShellEnvironmentInheritNone,
		IgnoreDefaultExcludes: true,
		Set:                   map[string]string{"PATH": "C:\\Windows"},
	}
	got := mapFromEnv(ApplyShellEnvironmentPolicy(nil, policy))
	if !strings.EqualFold(got["PATHEXT"], ".COM;.EXE;.BAT;.CMD") {
		t.Fatalf("PATHEXT=%q", got["PATHEXT"])
	}
}

func mapFromEnv(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
