package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHunkTrackerFileContentViews(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{
		"text.txt": "baseline\n", "binary.bin": "text baseline\n",
	})
	baselineLFS := "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 3\n"
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pointer.dat"), []byte(baselineLFS), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "binary.bin", "pointer.dat")
	runGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "special baselines")
	if err := os.WriteFile(filepath.Join(root, "text.txt"), []byte("current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'c', 0, 'd'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", maxHunkContentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	lfs := "version https://git-lfs.github.com/spec/v1\noid sha256:def\nsize 4\n"
	if err := os.WriteFile(filepath.Join(root, "pointer.dat"), []byte(lfs), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := os.Symlink("text.txt", filepath.Join(root, "link.txt")) == nil

	data, err := registry.HunkTracker().FileData(context.Background(), "text.txt", "all")
	if err != nil || data.Baseline.Status != "full" || data.Current.Status != "full" || data.Baseline.Content == nil || *data.Baseline.Content != "baseline\n" || data.Current.Content == nil || *data.Current.Content != "current\n" {
		t.Fatalf("text file data=%#v err=%v", data, err)
	}
	if data.BaselineContent == nil || data.CurrentContent == nil {
		t.Fatalf("legacy content fields missing: %#v", data)
	}
	entries, err := registry.HunkTracker().AllFileContents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]FileContentEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	if byPath["binary.bin"].Baseline.Status != "binary" || byPath["binary.bin"].Current.Status != "binary" || byPath["binary.bin"].Current.ByteLen == nil || *byPath["binary.bin"].Current.ByteLen != 3 {
		t.Fatalf("binary entry=%#v", byPath["binary.bin"])
	}
	if byPath["large.txt"].Baseline.Status != "missing" || byPath["large.txt"].Current.Status != "tooLarge" || byPath["large.txt"].Current.ByteLen == nil || *byPath["large.txt"].Current.ByteLen != maxHunkContentBytes+1 {
		t.Fatalf("large entry=%#v", byPath["large.txt"])
	}
	if byPath["pointer.dat"].Baseline.Status != "lfsPointer" || byPath["pointer.dat"].Current.Status != "lfsPointer" || byPath["pointer.dat"].Current.Content != nil {
		t.Fatalf("LFS entry=%#v", byPath["pointer.dat"])
	}
	if symlink && byPath["link.txt"].Current.Status != "symlink" {
		t.Fatalf("symlink entry=%#v", byPath["link.txt"])
	}
}

func TestHunkTrackerAllFileContentsKeepsAcceptedPaths(t *testing.T) {
	_, registry := newHunkFixture(t, map[string]string{"accepted.txt": "before\n"})
	if _, err := registry.Execute(context.Background(), "edit_file", []byte(`{"path":"accepted.txt","old_text":"before","new_text":"after"}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "accepted.txt", "all")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("hunks=%#v err=%v", hunks, err)
	}
	if _, err := registry.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "accept"); err != nil {
		t.Fatal(err)
	}
	entries, err := registry.HunkTracker().AllFileContents(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Path != "accepted.txt" || !entries[0].IsAgentFile {
		t.Fatalf("accepted contents=%#v err=%v", entries, err)
	}
}
