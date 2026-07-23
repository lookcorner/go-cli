package personas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListMergesScopesWithBundledPrecedence(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	writePersona(t, filepath.Join(home, "bundled", "personas", "shared.toml"), "description = \"bundled\"\n[[inputs]]\nname = \"topic\"\n")
	writePersona(t, filepath.Join(workspace, ".grok", "personas", "shared.toml"), "description = \"project\"\n")
	writePersona(t, filepath.Join(workspace, ".grok", "personas", "local.toml"), "instructions = '''First paragraph.\n\nSecond.'''\n[[outputs]]\nname = \"report\"\n")
	writePersona(t, filepath.Join(home, "personas", "user.toml"), "description = \"user\"\n")
	writePersona(t, filepath.Join(home, "personas", "broken.toml"), "[")

	items, err := New(workspace).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Name != "shared" || items[0].Scope != ScopeBundled || items[0].Description != "bundled" || !items[0].HasInputs {
		t.Fatalf("items=%#v", items)
	}
	if items[1].Name != "local" || items[1].Description != "First paragraph." || !items[1].HasOutputs || !items[1].Editable() {
		t.Fatalf("project persona=%#v", items[1])
	}
	if items[2].Name != "user" || items[2].Scope != ScopeUser {
		t.Fatalf("user persona=%#v", items[2])
	}
}

func TestCreateUsesSafeExclusiveFile(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	service := New(workspace)
	created, err := service.Create(Draft{Name: "code reviewer!", Description: "Reviews", Instructions: "Be strict", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "code-reviewer-" || created.Scope != ScopeProject || created.Description != "Reviews" {
		t.Fatalf("created=%#v", created)
	}
	info, err := os.Stat(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	content, err := service.Read(created.Path)
	if err != nil || !strings.Contains(content, "instructions = 'Be strict'") && !strings.Contains(content, "instructions = \"Be strict\"") {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, err := service.Create(Draft{Name: "code reviewer!", Scope: ScopeProject}); err == nil {
		t.Fatal("duplicate persona was overwritten")
	}
	if _, err := service.Create(Draft{Name: "///", Scope: ScopeUser}); err == nil {
		t.Fatal("invalid name was accepted")
	}
}

func TestDeleteAndReadStayWithinKnownDirectories(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	service := New(workspace)
	local := filepath.Join(home, "personas", "local.toml")
	bundled := filepath.Join(home, "bundled", "personas", "bundled.toml")
	outside := filepath.Join(t.TempDir(), "outside.toml")
	for _, path := range []string{local, bundled, outside} {
		writePersona(t, path, "instructions = \"x\"\n")
	}
	if _, err := service.Read(outside); err == nil {
		t.Fatal("read escaped persona roots")
	}
	if err := service.Delete(bundled); err == nil {
		t.Fatal("bundled persona was deleted")
	}
	if err := service.Delete(outside); err == nil {
		t.Fatal("delete escaped persona roots")
	}
	if err := service.Delete(local); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("local persona still exists: %v", err)
	}
}

func TestDeleteRejectsWritableDirectorySymlinkedToBundled(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	bundledDir := filepath.Join(home, "bundled", "personas")
	bundled := filepath.Join(bundledDir, "protected.toml")
	writePersona(t, bundled, "instructions = \"x\"\n")
	if err := os.Symlink(bundledDir, filepath.Join(home, "personas")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := New(workspace).Delete(filepath.Join(home, "personas", "protected.toml")); err == nil {
		t.Fatal("bundled persona was deleted through user directory symlink")
	}
	if _, err := os.Stat(bundled); err != nil {
		t.Fatalf("bundled persona changed: %v", err)
	}
}

func TestListSkipsPersonaSymlinkOutsideRoot(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	outside := filepath.Join(t.TempDir(), "outside.toml")
	writePersona(t, outside, "description = \"secret\"\n")
	dir := filepath.Join(home, "personas")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.toml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	items, err := New(workspace).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("escaped persona was listed: %#v", items)
	}
}

func TestCreateAndDeleteRejectWritableDirectorySymlinkEscape(t *testing.T) {
	home, workspace, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	personaDir := filepath.Join(workspace, ".grok", "personas")
	if err := os.MkdirAll(filepath.Dir(personaDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, personaDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	service := New(workspace)
	if _, err := service.Create(Draft{Name: "escaped", Scope: ScopeProject}); err == nil {
		t.Fatal("persona was created outside the project root")
	}
	escaped := filepath.Join(outside, "escaped.toml")
	writePersona(t, escaped, "instructions = \"x\"\n")
	if err := service.Delete(filepath.Join(personaDir, "escaped.toml")); err == nil {
		t.Fatal("persona was deleted outside the project root")
	}
	if _, err := os.Stat(escaped); err != nil {
		t.Fatalf("outside persona changed: %v", err)
	}
}

func TestUpdatePreservesUnknownFieldsAndRenamesSafely(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(workspace, ".grok", "personas", "old.toml")
	writePersona(t, path, "name = \"old\"\ndescription = \"old description\"\ninstructions = \"old instructions\"\nmodel = \"grok\"\n[[inputs]]\nname = \"topic\"\n")
	updated, err := New(workspace).Update(path, Draft{Name: "new", Description: "new description", Instructions: "new instructions", Scope: ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "new" || updated.Description != "new description" || updated.Model != "grok" || !updated.HasInputs {
		t.Fatalf("updated=%#v", updated)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old path still exists: %v", err)
	}
}

func TestUpdateRejectsBundledPersona(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "bundled", "personas", "built-in.toml")
	writePersona(t, path, "description = \"read only\"\n")
	if _, err := New(workspace).Update(path, Draft{Name: "built-in", Description: "changed"}); err == nil {
		t.Fatal("bundled persona was edited")
	}
}

func writePersona(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
