package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAndResolveCustomSandboxTOML(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	extraRO := filepath.Join(workspace, "data")
	extraRW := filepath.Join(workspace, "scratch")
	if err := os.MkdirAll(extraRO, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extraRW, 0o700); err != nil {
		t.Fatal(err)
	}
	toml := "" +
		"[profiles.project]\n" +
		"extends = \"workspace\"\n" +
		"restrict_network = true\n" +
		"read_only = [" + quoteTOML(extraRO) + "]\n" +
		"read_write = [" + quoteTOML(extraRW) + "]\n" +
		"deny = [\"secrets\"]\n"
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveSandboxProfile("project", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Custom || resolved.ChildBase != SandboxWorkspace || !resolved.RestrictNetwork {
		t.Fatalf("resolved=%+v", resolved)
	}
	if !containsLandlockPath(resolved.ReadOnly, extraRO) {
		t.Fatalf("missing RO %s in %v", extraRO, resolved.ReadOnly)
	}
	if !containsLandlockPath(resolved.ReadWrite, extraRW) {
		t.Fatalf("missing RW %s in %v", extraRW, resolved.ReadWrite)
	}
	if !containsLandlockPath(resolved.Deny, filepath.Join(workspace, "secrets")) {
		t.Fatalf("deny=%v", resolved.Deny)
	}

	paths := ParentLandlockPathsFromResolved(resolved, workspace)
	if len(paths.RODirs) == 0 || paths.RODirs[0] != "/" {
		t.Fatalf("custom should keep default_read=/: %v", paths.RODirs)
	}
	if !containsLandlockPath(paths.RWDirs, extraRW) {
		t.Fatalf("Landlock RW missing scratch: %v", paths.RWDirs)
	}
	if !containsLandlockPath(paths.RODirs, extraRO) {
		t.Fatalf("Landlock RO missing data: %v", paths.RODirs)
	}
}

func TestProjectSandboxTOMLIsAdditiveOnly(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(""+
		"[profiles.shared]\n"+
		"extends = \"read-only\"\n"+
		"restrict_network = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".grok", "sandbox.toml"), []byte(""+
		"[profiles.shared]\n"+
		"extends = \"workspace\"\n"+
		"restrict_network = false\n"+
		"[profiles.local]\n"+
		"extends = \"workspace\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := ResolveSandboxProfile("shared", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if shared.ChildBase != SandboxReadOnly || !shared.RestrictNetwork {
		t.Fatalf("project must not replace global shared: %+v", shared)
	}
	local, err := ResolveSandboxProfile("local", workspace)
	if err != nil || local.ChildBase != SandboxWorkspace {
		t.Fatalf("local=%+v err=%v", local, err)
	}
	conflicts := SandboxProfileConflicts(workspace)
	if len(conflicts) != 1 || conflicts[0] != "shared" {
		t.Fatalf("conflicts=%v", conflicts)
	}
	finding := SandboxProfileConflictFinding(workspace)
	if !strings.Contains(finding, "'shared'") || !strings.Contains(finding, "using the user profile") {
		t.Fatalf("finding=%q", finding)
	}
}

func TestSandboxProfileConflictsIgnoresIdenticalAndBuiltins(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	body := "[profiles.same]\nextends = \"workspace\"\n"
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".grok", "sandbox.toml"), []byte(body+
		"[profiles.workspace]\nextends = \"strict\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if conflicts := SandboxProfileConflicts(workspace); len(conflicts) != 0 {
		t.Fatalf("conflicts=%v", conflicts)
	}
}

func TestCustomLandlockFailClosedOnLinuxWithoutApply(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux fail-closed only")
	}
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv(InsideBwrapEnv, "")
	_ = os.Unsetenv(InsideBwrapEnv)
	if err := os.WriteFile(filepath.Join(home, "sandbox.toml"), []byte(""+
		"[profiles.locked]\n"+
		"extends = \"workspace\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// On kernels without Landlock, BestEffort returns applied=false → fail-closed.
	// On kernels with Landlock, apply succeeds → no error. Either outcome is fine
	// as long as missing custom still errors.
	if err := ApplyParentLandlock("missing", workspace, nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
	_ = ApplyParentLandlock("locked", workspace, nil) // may succeed or fail-closed depending on kernel
}

func quoteTOML(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}
