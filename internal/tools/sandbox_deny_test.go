package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiresReadDeny(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if RequiresReadDeny("workspace", workspace) || RequiresReadDeny("off", workspace) {
		t.Fatal("built-ins must not require read-deny")
	}
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(""+
		"[profiles.locked]\n"+
		"extends = \"workspace\"\n"+
		"deny = [\"secrets\"]\n"+
		"[profiles.open]\n"+
		"extends = \"workspace\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !RequiresReadDeny("locked", workspace) {
		t.Fatal("custom with deny should require read-deny")
	}
	if RequiresReadDeny("open", workspace) {
		t.Fatal("custom without deny should not require read-deny")
	}
}

func TestBuildReadDenyTargetsExactAndGlob(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	secret := filepath.Join(workspace, "secrets")
	if err := os.MkdirAll(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	pem := filepath.Join(workspace, "nested", "key.pem")
	if err := os.MkdirAll(filepath.Dir(pem), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pem, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(""+
		"[profiles.locked]\n"+
		"extends = \"workspace\"\n"+
		"deny = [\"secrets\", \"**/*.pem\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets, err := BuildReadDenyTargets("locked", workspace)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(targets, "\n")
	if !strings.Contains(joined, secret) {
		t.Fatalf("missing exact deny %s in %v", secret, targets)
	}
	if !strings.Contains(joined, pem) {
		t.Fatalf("missing glob deny %s in %v", pem, targets)
	}
}

func TestPartitionDenyEntries(t *testing.T) {
	exact, globs := partitionDenyEntries([]string{"secrets", "**/*.pem", "/abs/path", "/tmp/**/*.key", ""})
	if len(exact) != 2 || exact[0] != "secrets" || exact[1] != "/abs/path" {
		t.Fatalf("exact=%v", exact)
	}
	if len(globs) != 2 || globs[0] != "**/*.pem" || globs[1] != "/tmp/**/*.key" {
		t.Fatalf("globs=%v", globs)
	}
}

func TestBuildParentBwrapPlanIncludesDeny(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hooks-paths"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	denyPath := filepath.Join(workspace, "blocked.txt")
	if err := os.WriteFile(denyPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(""+
		"[profiles.locked]\n"+
		"extends = \"workspace\"\n"+
		"deny = [\"blocked.txt\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildParentBwrapPlan("locked", workspace, home, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Hooks.Leaves) == 0 {
		t.Fatal("expected hook leaves")
	}
	if len(plan.DenyRead) != 1 || plan.DenyRead[0] != denyPath {
		t.Fatalf("deny=%v", plan.DenyRead)
	}
}
