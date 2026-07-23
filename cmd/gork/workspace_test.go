package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	if got, err := resolveWorkspacePath(dir, ""); err != nil || got != filepath.Clean(dir) {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveWorkspacePath("child", dir); err != nil || got != child {
		t.Fatalf("relative resolved=%q err=%v", got, err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePath(file, dir); err == nil {
		t.Fatal("file accepted as workspace")
	}
}
