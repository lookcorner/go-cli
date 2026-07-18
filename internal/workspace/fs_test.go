package workspace

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestFSListFiltersSortsAndPaginates(t *testing.T) {
	root := t.TempDir()
	writeFSFixture(t, root, "a.txt", "a")
	writeFSFixture(t, root, "z.txt", "z")
	writeFSFixture(t, root, ".hidden", "hidden")
	writeFSFixture(t, root, "dir/nested.go", "package nested")
	writeFSFixture(t, root, "dir/skip.txt", "skip")
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	depthOne, err := ws.List(".", FSListOptions{Depth: 1, IncludeHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fsNodeNames(depthOne.Nodes), []string{"dir", ".hidden", "a.txt", "z.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("depth one names=%v want=%v", got, want)
	}
	depthTwo, err := ws.List(".", FSListOptions{Depth: 2, IncludeHidden: false, IncludeGlobs: []string{"**/*.go", "*.txt"}, ExcludeGlobs: []string{"**/skip.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fsNodeNames(depthTwo.Nodes), []string{"a.txt", "nested.go", "z.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered names=%v want=%v", got, want)
	}
	page, err := ws.List(".", FSListOptions{Depth: 1, IncludeHidden: true, Offset: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fsNodeNames(page.Nodes), []string{".hidden", "a.txt"}; !reflect.DeepEqual(got, want) || !page.Truncated {
		t.Fatalf("page names=%v truncated=%v", got, page.Truncated)
	}
}

func TestFSListGitIgnoreAndExternalSymlink(t *testing.T) {
	root := t.TempDir()
	writeFSFixture(t, root, ".gitignore", "ignored.txt\n")
	writeFSFixture(t, root, "ignored.txt", "ignored")
	writeFSFixture(t, root, "kept.txt", "kept")
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	outside := t.TempDir()
	writeFSFixture(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ws.List(".", FSListOptions{Depth: 2, IncludeHidden: false, FollowSymlinks: true, RespectGitIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fsNodeNames(result.Nodes), []string{"kept.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names=%v want=%v", got, want)
	}
}

func TestFSExistsReadAndRanges(t *testing.T) {
	root := t.TempDir()
	writeFSFixture(t, root, "text.txt", "one\ntwo")
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{0xff, 0x00, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Exists("text.txt") || ws.Exists("../missing") || ws.Exists(filepath.Join(t.TempDir(), "outside")) {
		t.Fatal("exists did not enforce workspace confinement")
	}
	text, err := ws.Read("text.txt", 0, 0, 1024, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if text.Content != "one\ntwo" || text.LineCount == nil || *text.LineCount != 2 || text.Type != "text/plain" {
		t.Fatalf("unexpected text read: %#v", text)
	}
	part, err := ws.Read("text.txt", 1, 3, 1024, "", true)
	if err != nil || part.Content != "ne\n" || part.LineCount != nil {
		t.Fatalf("unexpected ranged read: %#v err=%v", part, err)
	}
	empty, err := ws.Read("text.txt", 2, 0, 1024, "", true)
	if err != nil || empty.Content != "" {
		t.Fatalf("unexpected zero-length read: %#v err=%v", empty, err)
	}
	binary, err := ws.Read("binary.dat", 0, 0, 1024, "", false)
	if err != nil || binary.ContentBase64 == nil || *binary.ContentBase64 != base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0x01}) || binary.Type != "application/octet-stream" {
		t.Fatalf("unexpected binary read: %#v err=%v", binary, err)
	}
	forced, err := ws.Read("text.txt", 1, 2, 1024, "base64", true)
	if err != nil || forced.ContentBase64 == nil || *forced.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("ne")) || forced.Type != "text/plain" {
		t.Fatalf("unexpected base64 range: %#v err=%v", forced, err)
	}
	if full, err := ws.Read("text.txt", 0, 0, 2, "", false); err != nil || full.Content != "one\ntwo" {
		t.Fatalf("maxBytes affected full read: %#v err=%v", full, err)
	}
}

func TestFSWriteAndDelete(t *testing.T) {
	root := t.TempDir()
	writeFSFixture(t, root, "existing.txt", "old")
	existing := filepath.Join(root, "existing.txt")
	if err := os.Chmod(existing, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write("existing.txt", "new", false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(existing)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
	if err := ws.Write("new/deep/file.txt", "created", true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "new/deep/file.txt")); err != nil || string(data) != "created" {
		t.Fatalf("created content=%q err=%v", data, err)
	}
	if err := ws.Delete("new/deep/file.txt"); err != nil || ws.Exists("new/deep/file.txt") {
		t.Fatalf("delete file err=%v", err)
	}
	if err := ws.Delete("new"); err == nil {
		t.Fatal("delete_file accepted a directory")
	}

	target := filepath.Join(root, "target.txt")
	writeFSFixture(t, root, "target.txt", "keep")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := ws.Delete("link.txt"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("symlink delete affected target: %q err=%v", data, err)
	}
}

func writeFSFixture(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fsNodeNames(nodes []FSNode) []string {
	names := make([]string, len(nodes))
	for i, node := range nodes {
		names[i] = node.Name
	}
	return names
}
