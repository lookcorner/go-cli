package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRewindStorePersistsPreviewsAndRestoresFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "rewind.jsonl")
	store, err := NewRewindStore(ws, storePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(storePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("rewind store permissions: info=%v err=%v", info, err)
	}
	if err := store.CaptureBefore(0, "existing.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("agent"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(0, "existing.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(1, "created.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "created.txt"), []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(1, "created.txt"); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewRewindStore(ws, storePath)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := reopened.Counts()
	if err != nil || counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("unexpected persisted checkpoint counts: %#v err=%v", counts, err)
	}
	preview, err := reopened.Preview(0)
	if err != nil || len(preview.CleanFiles) != 2 || len(preview.Conflicts) != 0 {
		t.Fatalf("unexpected clean preview: %#v err=%v", preview, err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("external"), 0o640); err != nil {
		t.Fatal(err)
	}
	preview, err = reopened.Preview(0)
	if err != nil || len(preview.Conflicts) != 1 || preview.Conflicts[0].ConflictType != "modified_externally" {
		t.Fatalf("external edit was not detected: %#v err=%v", preview, err)
	}
	reverted, _, err := reopened.Restore(0)
	if err != nil || len(reverted) != 2 {
		t.Fatalf("restore failed: reverted=%#v err=%v", reverted, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "existing.txt"))
	if err != nil || string(data) != "before" {
		t.Fatalf("existing file was not restored: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("created file was not removed: %v", err)
	}
	counts, err = reopened.Counts()
	if err != nil || len(counts) != 0 {
		t.Fatalf("restored checkpoints were not truncated: %#v err=%v", counts, err)
	}
}

func TestRewindStoreCapturesFirstBeforeAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRewindStore(ws, filepath.Join(t.TempDir(), "rewind.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(0, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("intermediate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(0, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("final"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(0, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Restore(0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatalf("first before-snapshot was replaced: %q", data)
	}
	if err := store.CaptureBefore(1, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(1, "file.txt"); err != nil {
		t.Fatal(err)
	}
	counts, err := store.Counts()
	if err != nil || counts[1] != 0 {
		t.Fatalf("cancelled checkpoint remained live: %#v err=%v", counts, err)
	}
	if err := store.CaptureBefore(1, "../escape.txt"); err == nil {
		t.Fatal("checkpoint accepted a path outside the workspace")
	}
}
