package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileResourceLifecycle(t *testing.T) {
	root := t.TempDir()
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "created.go")
	if changed, err := ws.CreateFile(created, false, false); err != nil || !changed {
		t.Fatalf("create changed=%v err=%v", changed, err)
	}
	if err := os.WriteFile(created, []byte("package created\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if changed, err := ws.CreateFile(created, false, true); err != nil || changed {
		t.Fatalf("ignored create changed=%v err=%v", changed, err)
	}
	if changed, err := ws.CreateFile(created, true, false); err != nil || !changed {
		t.Fatalf("overwrite changed=%v err=%v", changed, err)
	}
	if data, _ := os.ReadFile(created); len(data) != 0 {
		t.Fatalf("overwrite did not empty file: %q", data)
	}
	if err := os.WriteFile(created, []byte("source"), 0o640); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, "renamed.go")
	if err := os.WriteFile(renamed, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ws.RenameFile(created, renamed, false, true); err != nil || changed {
		t.Fatalf("ignored rename changed=%v err=%v", changed, err)
	}
	if changed, err := ws.RenameFile(created, renamed, true, false); err != nil || !changed {
		t.Fatalf("rename changed=%v err=%v", changed, err)
	}
	if data, _ := os.ReadFile(renamed); string(data) != "source" {
		t.Fatalf("rename overwrite stored %q", data)
	}
	if changed, err := ws.DeleteFile(renamed, false); err != nil || !changed {
		t.Fatalf("delete changed=%v err=%v", changed, err)
	}
	if changed, err := ws.DeleteFile(renamed, true); err != nil || changed {
		t.Fatalf("ignored delete changed=%v err=%v", changed, err)
	}
}

func TestWorkspaceFileResourcesRejectDirectoriesAndEscapes(t *testing.T) {
	root := t.TempDir()
	ws, _ := Open(root)
	if _, err := ws.DeleteFile(root, false); err == nil {
		t.Fatal("directory delete was accepted")
	}
	if _, err := ws.CreateFile(filepath.Join(root, "missing", "file.go"), false, false); err == nil {
		t.Fatal("create below missing parent was accepted")
	}
	if _, err := ws.RenameFile(filepath.Join(root, "missing.go"), filepath.Join(t.TempDir(), "outside.go"), false, false); err == nil {
		t.Fatal("escaping rename was accepted")
	}
}
