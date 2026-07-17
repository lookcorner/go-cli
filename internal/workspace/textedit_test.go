package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyTextEditsUsesUTF16AndPreservesMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(path, []byte("a😀b\r\nsecond\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := ws.ApplyTextEdits(path, []TextEdit{
		{Start: TextPosition{Line: 0, Character: 1}, End: TextPosition{Line: 0, Character: 3}, NewText: "X"},
		{Start: TextPosition{Line: 1, Character: 0}, End: TextPosition{Line: 1, Character: 6}, NewText: "2nd"},
	})
	if err != nil || content != "aXb\r\n2nd\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil {
		t.Fatalf("readErr=%v statErr=%v", err, statErr)
	}
	if string(data) != content || info.Mode().Perm() != 0o640 {
		t.Fatalf("stored=%q mode=%v", data, info.Mode().Perm())
	}
}

func TestApplyTextEditsRejectsOverlapAndSurrogateSplit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "edit.txt")
	original := "a😀bc\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, _ := Open(root)
	_, err := ws.ApplyTextEdits(path, []TextEdit{
		{Start: TextPosition{Line: 0, Character: 0}, End: TextPosition{Line: 0, Character: 3}, NewText: "one"},
		{Start: TextPosition{Line: 0, Character: 2}, End: TextPosition{Line: 0, Character: 4}, NewText: "two"},
	})
	if err == nil {
		t.Fatal("overlapping edits were accepted")
	}
	if _, err := ws.ApplyTextEdits(path, []TextEdit{
		{Start: TextPosition{Line: 0}, End: TextPosition{Line: 0}, NewText: "insert"},
		{Start: TextPosition{Line: 0}, End: TextPosition{Line: 0, Character: 1}, NewText: "replace"},
	}); err == nil {
		t.Fatal("insertion overlapping a replacement was accepted")
	}
	if _, err := ws.ApplyTextEdits(path, []TextEdit{{
		Start: TextPosition{Line: 0, Character: 2}, End: TextPosition{Line: 0, Character: 3}, NewText: "split",
	}}); err == nil {
		t.Fatal("UTF-16 surrogate split was accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("rejected edits changed file: %q", data)
	}
}

func TestApplyTextEditsRejectsEscapingSymlink(t *testing.T) {
	root, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ws, _ := Open(root)
	if _, err := ws.ApplyTextEdits(link, []TextEdit{{NewText: "changed"}}); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "outside" {
		t.Fatalf("outside file changed: %q", data)
	}
}
