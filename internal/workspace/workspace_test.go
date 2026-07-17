package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfinesPaths(t *testing.T) {
	root := t.TempDir()
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ws.Resolve("inside.txt")
	expected, realErr := filepath.EvalSymlinks(inside)
	if realErr != nil {
		t.Fatal(realErr)
	}
	if err != nil || resolved != expected {
		t.Fatalf("resolve inside path: path=%q err=%v", resolved, err)
	}
	if _, err := ws.Resolve("../outside.txt"); err == nil {
		t.Fatal("expected parent traversal to be rejected")
	}
}

func TestResolveRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Resolve("escape"); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}
