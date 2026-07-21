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

func TestResolveEntryPreservesFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ws.ResolveEntry("link.txt")
	if err != nil || resolved != filepath.Join(ws.Root(), "link.txt") {
		t.Fatalf("resolved entry=%q err=%v", resolved, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("final symlink was followed: mode=%v", info.Mode())
	}
}

func TestWorkspaceExtraRootIsIsolatedAndConfined(t *testing.T) {
	root, extra, outside := t.TempDir(), t.TempDir(), t.TempDir()
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ws.WithExtraRoot(extra)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(extra, "MEMORY.md")
	if err := os.WriteFile(inside, []byte("memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	realInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.Resolve(inside); err != nil || got != realInside {
		t.Fatalf("extra root resolve=%q err=%v", got, err)
	}
	if _, err := ws.Resolve(inside); err == nil {
		t.Fatal("base workspace inherited the extra root")
	}
	if got, err := view.Resolve("relative.txt"); err != nil || got != filepath.Join(ws.Root(), "relative.txt") {
		t.Fatalf("relative resolve=%q err=%v", got, err)
	}
	if _, err := view.Resolve(filepath.Join(outside, "escape.txt")); err == nil {
		t.Fatal("unrelated absolute path escaped confinement")
	}
	if err := os.Symlink(outside, filepath.Join(extra, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Resolve(filepath.Join(extra, "escape")); err == nil {
		t.Fatal("extra-root symlink escaped confinement")
	}
}
