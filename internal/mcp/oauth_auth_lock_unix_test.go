//go:build unix

package mcp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPAuthLockPathSanitizesName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := mcpAuthLockPath("Linear MCP!")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "mcp_auth_Linear_MCP_.lock" || !strings.HasPrefix(path, home) {
		t.Fatalf("path=%q", path)
	}
}
