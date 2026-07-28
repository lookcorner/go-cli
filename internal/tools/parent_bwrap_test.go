package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiresHookWriteDeny(t *testing.T) {
	if RequiresHookWriteDeny("off") || RequiresHookWriteDeny("") {
		t.Fatal("off should not require hook write-deny")
	}
	for _, profile := range []string{"workspace", "read-only", "strict"} {
		if !RequiresHookWriteDeny(profile) {
			t.Fatalf("%s should require hook write-deny", profile)
		}
	}
}

func TestBuildHookWriteDenyPlan(t *testing.T) {
	home := t.TempDir()
	hooks := filepath.Join(home, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	hookFile := filepath.Join(hooks, "pre.json")
	if err := os.WriteFile(hookFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(t.TempDir(), "extra.json")
	if err := os.WriteFile(extra, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hooks-paths"), []byte(extra+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildHookWriteDenyPlan(home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Leaves, "\n")
	for _, want := range []string{hooks, hookFile, filepath.Join(home, "hooks-paths"), extra} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing leaf %s in %#v", want, plan.Leaves)
		}
	}
	if len(plan.AncestorRW) == 0 {
		t.Fatal("expected ancestor binds")
	}
}

func TestEnsureParentBwrapOffIsNoop(t *testing.T) {
	if err := EnsureParentBwrapHookWriteDeny("off", t.TempDir(), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestIsInsideBwrap(t *testing.T) {
	t.Setenv(InsideBwrapEnv, "")
	os.Unsetenv(InsideBwrapEnv)
	if IsInsideBwrap() {
		t.Fatal("expected outside")
	}
	t.Setenv(InsideBwrapEnv, "1")
	if !IsInsideBwrap() {
		t.Fatal("expected inside")
	}
}
