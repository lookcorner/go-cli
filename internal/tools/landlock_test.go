package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParentLandlockPathsWorkspaceAndReadOnly(t *testing.T) {
	workspace := t.TempDir()
	ws := ParentLandlockPathsFor(SandboxWorkspace, workspace)
	if len(ws.RODirs) == 0 || ws.RODirs[0] != "/" {
		t.Fatalf("workspace RO=%v", ws.RODirs)
	}
	if !containsLandlockPath(ws.RWDirs, workspace) {
		t.Fatalf("workspace RW missing workspace: %v", ws.RWDirs)
	}
	ro := ParentLandlockPathsFor(SandboxReadOnly, workspace)
	if len(ro.RODirs) == 0 || ro.RODirs[0] != "/" {
		t.Fatalf("read-only RO=%v", ro.RODirs)
	}
	if containsLandlockPath(ro.RWDirs, workspace) {
		t.Fatalf("read-only must not write workspace: %v", ro.RWDirs)
	}
}

func TestParentLandlockPathsStrict(t *testing.T) {
	workspace := t.TempDir()
	paths := ParentLandlockPathsFor(SandboxStrict, workspace)
	if containsLandlockPath(paths.RODirs, "/") && len(paths.RODirs) == 1 {
		t.Fatalf("strict should not be default_read=/: %v", paths.RODirs)
	}
	if infoExists("/usr") && !containsLandlockPath(paths.RODirs, "/usr") {
		t.Fatalf("strict RO missing /usr: %v", paths.RODirs)
	}
	if !containsLandlockPath(paths.RWDirs, workspace) {
		t.Fatalf("strict RW missing workspace: %v", paths.RWDirs)
	}
	if !containsLandlockPath(paths.RODirs, workspace) && !containsLandlockPath(paths.RWDirs, workspace) {
		t.Fatalf("strict must allow reading workspace")
	}
}

func TestApplyParentLandlockOffAndInvalid(t *testing.T) {
	if err := ApplyParentLandlock("off", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if err := ApplyParentLandlock("missing-custom", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v", err)
	}
	// Applying workspace Landlock to this process would poison later package
	// tests on Linux (e.g. block /dev/null). Exercise apply in a child only.
	runLandlockChild(t, "TestApplyParentLandlockWorkspaceChild")
}

func TestApplyParentLandlockWorkspaceChild(t *testing.T) {
	if os.Getenv("GORK_LANDLOCK_CHILD") != "1" {
		t.Skip("landlock apply child only")
	}
	var buf strings.Builder
	if err := ApplyParentLandlock("workspace", t.TempDir(), &buf); err != nil {
		t.Fatal(err)
	}
}

func runLandlockChild(t *testing.T, testName string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GORK_LANDLOCK_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("landlock child %s failed: %v\n%s", testName, err, out)
	}
}

func containsLandlockPath(paths []string, want string) bool {
	want = filepath.Clean(want)
	for _, path := range paths {
		if filepath.Clean(path) == want {
			return true
		}
	}
	return false
}

func infoExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
