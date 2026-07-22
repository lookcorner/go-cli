package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func newHashlineRegistry(t *testing.T, root string) *Registry {
	t.Helper()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	if err := registry.ConfigureFileToolset("hashline", "chunk", 3, 8); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func readHashline(t *testing.T, registry *Registry, path string) string {
	t.Helper()
	output, err := registry.Execute(context.Background(), "hashline_read", json.RawMessage(`{"target_file":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func anchorAt(output string, line int) string {
	prefix := string(rune('0'+line)) + ":"
	for _, value := range strings.Split(output, "\n") {
		if strings.HasPrefix(value, prefix) {
			return strings.SplitN(value, "→", 2)[0]
		}
	}
	return ""
}

func TestHashlineToolsetIsExclusiveAndRebindsWorkspace(t *testing.T) {
	parentRoot, childRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(childRoot, "child.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := newHashlineRegistry(t, parentRoot)
	for _, name := range []string{"hashline_read", "hashline_edit", "hashline_grep"} {
		if !parent.HasTool(name) {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"read_file", "grep", "search_replace", "write_file", "edit_file"} {
		if parent.HasTool(name) {
			t.Fatalf("standard tool %s bypasses hashline mode", name)
		}
	}
	childWS, err := workspace.Open(childRoot)
	if err != nil {
		t.Fatal(err)
	}
	child := parent.ForWorkspace(childWS)
	defer child.Close()
	if output := readHashline(t, child, "child.txt"); !strings.Contains(output, "→child") {
		t.Fatalf("child output=%q", output)
	}
	view := parent.View(nil, nil, "read-write")
	viewChild := view.ForWorkspace(childWS)
	defer viewChild.Close()
	if !viewChild.HasTool("hashline_edit") || viewChild.HasTool("write_file") {
		t.Fatal("view workspace binding lost the hashline toolset")
	}
}

func TestHashlineAnchorsNormalizeWhitespaceAndDetectChunkChanges(t *testing.T) {
	config := defaultHashlineConfig()
	first := config.anchors([]string{"  let   x = 1 ", "next"})
	second := config.anchors([]string{"let x = 1", "changed"})
	if first[0].local != second[0].local {
		t.Fatalf("formatter-only change altered local hash: %s != %s", first[0].local, second[0].local)
	}
	if first[0].context == second[0].context {
		t.Fatal("chunk change did not invalidate contextual anchor")
	}
	contentOnly, err := newHashlineConfig("content_only", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(contentOnly.anchors([]string{"line"})[0].String(), ":") != 1 {
		t.Fatal("content_only emitted a context hash")
	}
}

func TestHashlineGrepAddsAnchorsAndPreservesSeparators(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("before\nmatch me\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newHashlineRegistry(t, root)
	output, err := registry.Execute(context.Background(), "hashline_grep", json.RawMessage(`{"pattern":"match","path":"sample.txt","-C":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`sample\.txt[:\-]2:[a-z]{3}:[a-z]{3}:match me`).MatchString(output) {
		t.Fatalf("match anchor missing: %q", output)
	}
	if !regexp.MustCompile(`sample\.txt[:\-]1:[a-z]{3}:[a-z]{3}-before`).MatchString(output) {
		t.Fatalf("context separator missing: %q", output)
	}
}

func TestHashlineEditValidatesWholeBatchBeforeWriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newHashlineRegistry(t, root)
	read := readHashline(t, registry, "sample.txt")
	first := anchorAt(read, 1)
	arguments := `{"file_path":"sample.txt","edits":[` +
		`{"op":"replace","anchor":"` + first + `","content":"ONE"},` +
		`{"op":"replace","anchor":"2:aaa:bbb","content":"TWO"}]}`
	if _, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(arguments)); err == nil || !strings.Contains(err.Error(), "no edits were applied") {
		t.Fatalf("expected stale batch error, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("partial batch was written: %q", data)
	}
}

func TestHashlineEditAppliesBottomUpAndRejectsOverlaps(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree"), 0o640); err != nil {
		t.Fatal(err)
	}
	registry := newHashlineRegistry(t, root)
	read := readHashline(t, registry, "sample.txt")
	one, two, three := anchorAt(read, 1), anchorAt(read, 2), anchorAt(read, 3)
	overlap := `{"file_path":"sample.txt","edits":[` +
		`{"op":"replace","anchor":"` + one + `","end_anchor":"` + two + `","content":"x"},` +
		`{"op":"replace","anchor":"` + two + `","end_anchor":"` + three + `","content":"y"}]}`
	if _, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(overlap)); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
	edits := `{"file_path":"sample.txt","edits":[` +
		`{"op":"insert_after","anchor":"` + one + `","content":"middle"},` +
		`{"op":"replace","anchor":"` + three + `","content":"THREE"}]}`
	output, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(edits))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "→middle") || !strings.Contains(output, "→THREE") {
		t.Fatalf("fresh anchors missing: %q", output)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one\nmiddle\ntwo\nTHREE" {
		t.Fatalf("bottom-up result=%q", data)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
			t.Fatalf("mode=%o", info.Mode().Perm())
		}
	}
}

func TestHashlineEditPreservesCRLFAndSupportsSentinels(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newHashlineRegistry(t, root)
	arguments := `{"file_path":"sample.txt","edits":[` +
		`{"op":"insert_after","anchor":"0:","content":"zero"},` +
		`{"op":"insert_after","anchor":"EOF","content":"three"}]}`
	if _, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(arguments)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "zero\r\none\r\ntwo\r\nthree\r\n" || strings.Contains(string(data), "\n\n") {
		t.Fatalf("CRLF result=%q", data)
	}
}

func TestHashlineEditFailsClosedWhenCheckpointFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newHashlineRegistry(t, root)
	storePath := filepath.Join(t.TempDir(), "rewind.jsonl")
	store, err := workspace.NewRewindStore(registry.readFile.ws, storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(storePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.jsonl"), storePath); err != nil {
		t.Fatal(err)
	}
	registry.SetRewindStore(store, func() int { return 0 })
	arguments := `{"file_path":"sample.txt","edits":[{"op":"write","content":"new"}]}`
	if _, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(arguments)); err == nil || !strings.Contains(err.Error(), "checkpoint before") {
		t.Fatalf("checkpoint error=%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatalf("write escaped checkpoint: %q", data)
	}
}

type mutatingApprover struct{ path string }

func (a mutatingApprover) Approve(context.Context, string, string) error {
	return os.WriteFile(a.path, []byte("external"), 0o600)
}

func TestHashlineEditRejectsChangeDuringApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, mutatingApprover{path: path})
	defer registry.Close()
	if err := registry.ConfigureFileToolset("hashline", "chunk", 3, 8); err != nil {
		t.Fatal(err)
	}
	read := readHashline(t, registry, "sample.txt")
	arguments := `{"file_path":"sample.txt","edits":[{"op":"replace","anchor":"` + anchorAt(read, 1) + `","content":"agent"}]}`
	if _, err := registry.Execute(context.Background(), "hashline_edit", json.RawMessage(arguments)); err == nil || !strings.Contains(err.Error(), "changed after anchor validation") {
		t.Fatalf("race error=%v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "external" {
		t.Fatalf("external update was overwritten: %q", data)
	}
}
