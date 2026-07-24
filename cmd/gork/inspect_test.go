package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectCLIJSONAndHumanOutput(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Inspect instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	var jsonOutput, stderr bytes.Buffer
	if err := runInspect([]string{"--json"}, &jsonOutput, &stderr); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["cwd"] != canonicalRoot || !strings.Contains(strings.ToLower(jsonOutput.String()), "agents.md") {
		t.Fatalf("json output=%s", jsonOutput.String())
	}

	var human bytes.Buffer
	if err := runInspect(nil, &human, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Working directory: " + canonicalRoot, "Project instructions (1):", "Skills (", "MCP servers ("} {
		if !strings.Contains(human.String(), expected) {
			t.Fatalf("human output missing %q:\n%s", expected, human.String())
		}
	}
}

func TestInspectCLIRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runInspect([]string{"extra"}, &stdout, &stderr); err == nil {
		t.Fatal("inspect accepted a positional argument")
	}
}
