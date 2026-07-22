package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBuildFileIndexFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	writeFSFixture(t, root, ".gitignore", "ignored.txt\n")
	writeFSFixture(t, root, "ignored.txt", "ignored")
	writeFSFixture(t, root, "cache/skip.tmp", "skip")
	writeFSFixture(t, root, "cache/keep.tmp", "keep")
	writeFSFixture(t, root, "src/main.go", "package main")
	writeFSFixture(t, root, ".hidden/secret", "secret")
	index, err := BuildFileIndex(context.Background(), root, []string{"*.tmp", "!keep.tmp"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range index.Entries() {
		paths = append(paths, entry.Path)
	}
	want := []string{"cache", "cache/keep.tmp", "src", "src/main.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v want=%v", paths, want)
	}
}

func TestCustomIgnoreUsesLastMatchingRule(t *testing.T) {
	if customIgnored("keep.tmp", []string{"*.tmp", "!keep.tmp"}) {
		t.Fatal("later include did not restore the path")
	}
	if !customIgnored("keep.tmp", []string{"!keep.tmp", "*.tmp"}) {
		t.Fatal("later exclude did not ignore the path")
	}
}

func TestWatchFileIndexReportsCreateModifyAndRemove(t *testing.T) {
	root := t.TempDir()
	writeFSFixture(t, root, "existing.txt", "old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initial, err := BuildFileIndex(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan []FileChange, 8)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WatchFileIndex(ctx, initial, 10*time.Millisecond, nil, func() { close(ready) }, func(batch []FileChange) { changes <- batch })
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for file watcher")
	}

	created := filepath.Join(root, "new.txt")
	if err := os.WriteFile(created, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFileChange(t, changes, FileCreated, "new.txt")
	if err := os.WriteFile(created, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFileChange(t, changes, FileModified, "new.txt")
	if err := os.Remove(created); err != nil {
		t.Fatal(err)
	}
	waitForFileChange(t, changes, FileRemoved, "new.txt")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForFileChange(t *testing.T, batches <-chan []FileChange, kind FileChangeKind, path string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case batch := <-batches:
			for _, change := range batch {
				for _, entry := range change.Entries {
					if change.Kind == kind && entry.Path == path {
						return
					}
				}
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s %s", kind, path)
		}
	}
}
