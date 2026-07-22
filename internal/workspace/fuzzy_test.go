package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"
)

func TestFuzzyScoreUsesSmartCaseAndRuneIndices(t *testing.T) {
	score, indices, ok := fuzzyScore("svr", "cmd/Server.go")
	if !ok || score <= 0 || !slices.Equal(indices, []int{4, 7, 9}) {
		t.Fatalf("score=%d indices=%v ok=%v", score, indices, ok)
	}
	if _, _, ok := fuzzyScore("Server", "cmd/server.go"); ok {
		t.Fatal("uppercase query matched lowercase candidate")
	}
	if _, indices, ok := fuzzyScore("界面", "文档/界面.go"); !ok || !slices.Equal(indices, []int{3, 4}) {
		t.Fatalf("unicode indices=%v ok=%v", indices, ok)
	}
}

func TestFuzzySearchFilesFiltersSortsAndHonorsHidden(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"cmd/server.go": "package cmd\n", "cmd/search.go": "package cmd\n", "docs/server.md": "server\n",
		"generated/server.go": "package generated\n", ".secret-server": "hidden\n", ".gitignore": "generated/\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	matches, total, err := fuzzySearchFiles(context.Background(), root, "server", false, 2, false)
	if err != nil || total != 2 || len(matches) != 2 || matches[0].Score < matches[1].Score {
		t.Fatalf("matches=%#v total=%d err=%v", matches, total, err)
	}
	if !slices.Equal(matches[1].Indices, []int{5, 6, 7, 8, 9, 10}) {
		t.Fatalf("basename match indices=%v", matches[1].Indices)
	}
	for _, match := range matches {
		if !filepath.IsAbs(match.Path) || match.Type != "file" || match.Name == "server.go" && len(match.Indices) == 0 {
			t.Fatalf("unexpected match: %#v", match)
		}
	}
	directories, _, err := fuzzySearchFiles(context.Background(), root, "cmd/", true, 100, false)
	if err != nil || len(directories) != 1 || directories[0].Type != "directory" || directories[0].Name != "cmd" {
		t.Fatalf("directories=%#v err=%v", directories, err)
	}
	visible, _, _ := fuzzySearchFiles(context.Background(), root, "secret", false, 100, false)
	hidden, _, _ := fuzzySearchFiles(context.Background(), root, "secret", false, 100, true)
	if len(visible) != 0 || len(hidden) != 1 || hidden[0].Name != ".secret-server" {
		t.Fatalf("visible=%#v hidden=%#v", visible, hidden)
	}
	ignored, _, _ := fuzzySearchFiles(context.Background(), root, "generated", false, 100, false)
	if len(ignored) != 0 {
		t.Fatalf("ignored paths leaked: %#v", ignored)
	}
	top, total, _ := fuzzySearchFiles(context.Background(), root, "", false, 100, false)
	if total != 2 || len(top) != 2 || top[0].Type != "directory" || top[1].Type != "directory" {
		t.Fatalf("empty query returned non-top-level entries: %#v total=%d", top, total)
	}
}

func TestFuzzySearchManagerLifecycleAndRouting(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "search.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewFuzzySearchManager(time.Minute)
	defer manager.CloseAll()
	id, err := manager.Open(root, nil, false, FuzzyRouting{SessionID: "session-1", TargetClientID: &FuzzyClientID{InstanceID: "instance", ConnID: "connection"}})
	if err != nil || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(id) || id[14] != '7' {
		t.Fatalf("id=%q err=%v", id, err)
	}
	statuses := make(chan FuzzyStatus, 1)
	if !manager.Change(context.Background(), id, "search", false, 10, func(status FuzzyStatus) { statuses <- status }) {
		t.Fatal("open search was not found")
	}
	select {
	case status := <-statuses:
		if !status.Done || status.Generation != 1 || status.Total != 1 || len(status.Matches) != 1 || status.Routing.SessionID != "session-1" || status.Routing.TargetClientID.ConnID != "connection" {
			t.Fatalf("unexpected status: %#v", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fuzzy status")
	}
	if !manager.Close(id) || manager.Close(id) || manager.Change(context.Background(), id, "search", false, 10, nil) {
		t.Fatal("close was not idempotent")
	}
	requestID := "client-request"
	if got, err := manager.Open(root, &requestID, false, FuzzyRouting{}); err != nil || got != requestID {
		t.Fatalf("request ID=%q err=%v", got, err)
	}
	manager.CloseAll()
	if _, err := manager.Open(root, nil, false, FuzzyRouting{}); err == nil {
		t.Fatal("closed manager accepted a search")
	}
}
