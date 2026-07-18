package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestResolveFolderTrustPrecedence(t *testing.T) {
	home := canonicalOrCleanTrust(t.TempDir())
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTrustFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{}}`)
	store := filepath.Join(home, ".grok", trustFileName)

	if got := resolveFolderTrust(root, store, home, false, false, false); got != TrustTrusted {
		t.Fatalf("disabled gate = %s", got)
	}
	if got := resolveFolderTrust(root, store, home, true, true, false); got != TrustTrusted {
		t.Fatalf("development gate = %s", got)
	}
	if got := resolveFolderTrust(root, store, home, true, false, false); got != TrustUntrusted {
		t.Fatalf("headless release gate = %s", got)
	}
	if got := resolveFolderTrust(root, store, home, true, false, true); got != TrustPrompt {
		t.Fatalf("interactive release gate = %s", got)
	}
	if err := recordFolderTrust(context.Background(), store, root, true); err != nil {
		t.Fatal(err)
	}
	if got := resolveFolderTrust(root, store, home, true, false, false); got != TrustTrusted {
		t.Fatalf("stored trust = %s", got)
	}
}

func TestTrustStoreMostSpecificDecisionWins(t *testing.T) {
	home := canonicalOrCleanTrust(t.TempDir())
	parent := filepath.Join(home, "projects")
	child := filepath.Join(parent, "sensitive")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(home, ".grok", trustFileName)
	if err := recordFolderTrust(context.Background(), store, parent, true); err != nil {
		t.Fatal(err)
	}
	if err := recordFolderTrust(context.Background(), store, child, false); err != nil {
		t.Fatal(err)
	}
	document := readTrustDocument(store)
	if !trustedByStore(document, filepath.Join(parent, "normal"), home) {
		t.Fatal("parent trust did not cascade")
	}
	if trustedByStore(document, child, home) {
		t.Fatal("specific child denial did not override parent trust")
	}
	if trustedByStore(trustDocument{Folders: map[string]folderTrust{home: {Trusted: true}}}, child, home) {
		t.Fatal("unsafe home trust record was honored")
	}
}

func TestTrustStoreConcurrentWritersMerge(t *testing.T) {
	home := canonicalOrCleanTrust(t.TempDir())
	store := filepath.Join(home, ".grok", trustFileName)
	paths := []string{filepath.Join(home, "a"), filepath.Join(home, "b")}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var group sync.WaitGroup
	errors := make(chan error, len(paths))
	for _, path := range paths {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- recordFolderTrust(context.Background(), store, path, true)
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	document := readTrustDocument(store)
	for _, path := range paths {
		if !trustedByStore(document, path, home) {
			t.Fatalf("concurrent trust record missing for %q: %#v", path, document)
		}
	}
}

func TestWorkspaceTrustKeyUsesGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := WorkspaceTrustKey(nested), canonicalOrCleanTrust(root); got != want {
		t.Fatalf("workspace key=%q want=%q", got, want)
	}
}

func TestTrustStorePathUsesGrokHomeWithoutUserHome(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), "custom-grok")
	t.Setenv("GROK_HOME", grokHome)
	if got := trustStorePath(""); got != filepath.Join(grokHome, trustFileName) {
		t.Fatalf("trust store path=%q", got)
	}
}

func TestProjectExecutionConfigPresent(t *testing.T) {
	root := t.TempDir()
	if ProjectExecutionConfigPresent(root) {
		t.Fatal("empty workspace reported execution config")
	}
	writeTrustFile(t, filepath.Join(root, ".grok", "config.toml"), "[models]\ndefault='x'\n")
	if ProjectExecutionConfigPresent(root) {
		t.Fatal("non-executable project config triggered trust")
	}
	writeTrustFile(t, filepath.Join(root, ".grok", "config.toml"), "[plugins]\nenabled=['x']\n")
	if ProjectExecutionConfigPresent(root) {
		t.Fatal("plugin enablement without project paths triggered trust")
	}
	writeTrustFile(t, filepath.Join(root, ".grok", "config.toml"), "[plugins]\npaths=['./plugin']\n")
	if !ProjectExecutionConfigPresent(root) {
		t.Fatal("project plugin path did not trigger trust")
	}
	writeTrustFile(t, filepath.Join(root, ".grok", "config.toml"), "[mcp_servers.local]\ncommand='server'\n")
	if !ProjectExecutionConfigPresent(root) {
		t.Fatal("project MCP config did not trigger trust")
	}
}

func writeTrustFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
