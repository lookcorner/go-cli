package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateRenderMermaidPreservesOtherUISettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = 'grokday'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRenderMermaid(path, "off"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "render_mermaid = 'off'") || !strings.Contains(text, "theme = 'grokday'") {
		t.Fatalf("updated config:\n%s", text)
	}
	if err := UpdateRenderMermaid(path, "invalid"); err == nil {
		t.Fatal("invalid Mermaid mode was accepted")
	}
}
