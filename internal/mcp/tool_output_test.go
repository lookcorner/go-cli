package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestToolAdapterBoundsAndPersistsMCPOutput(t *testing.T) {
	artifacts := t.TempDir()
	adapter := &ToolAdapter{output: OutputConfig{MaxBytes: 11, ArtifactDir: artifacts}}
	full := `{"message":"` + strings.Repeat("x", 40) + `"}`
	ctx := tools.WithToolCall(context.Background(), "call/../unsafe", "mcp__fixture__echo")
	output := adapter.boundOutput(ctx, full)
	if !strings.HasPrefix(output, full[:11]) || !strings.Contains(output, "showing first 11 bytes") || !strings.Contains(output, "jq or Python") {
		t.Fatalf("bounded output=%q", output)
	}
	path := filepath.Join(artifacts, "mcp", "call____unsafe.json")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != full {
		t.Fatalf("saved output=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode=%v err=%v", info, err)
	}
}

func TestToolAdapterTruncatesAtUTF8Boundary(t *testing.T) {
	adapter := &ToolAdapter{output: OutputConfig{MaxBytes: 5}}
	output := adapter.boundOutput(context.Background(), "abc你好")
	preview := strings.Split(output, "\n\n")[0]
	if preview != "abc" || !utf8.ValidString(output) || strings.Contains(output, "Full output written") {
		t.Fatalf("UTF-8 output=%q", output)
	}
}

func TestToolAdapterLeavesSmallOutputUntouched(t *testing.T) {
	adapter := &ToolAdapter{output: OutputConfig{MaxBytes: 20_000, ArtifactDir: t.TempDir()}}
	if output := adapter.boundOutput(context.Background(), "done"); output != "done" {
		t.Fatalf("small output=%q", output)
	}
}

func TestToolAdapterUsesCallingSessionArtifacts(t *testing.T) {
	parent, child := t.TempDir(), t.TempDir()
	adapter := &ToolAdapter{serverName: "fixture", remoteName: "echo", output: OutputConfig{MaxBytes: 3, ArtifactDir: parent}}
	ctx := tools.WithToolCall(context.Background(), "child-call", "mcp__fixture__echo")
	ctx = tools.WithToolArtifactDir(ctx, child)
	adapter.boundOutput(ctx, "abcdef")
	if _, err := os.Stat(filepath.Join(child, "mcp", "child-call.txt")); err != nil {
		t.Fatalf("child artifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "mcp", "child-call.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent received child artifact: %v", err)
	}
}
