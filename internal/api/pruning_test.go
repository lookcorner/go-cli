package api

import (
	"strings"
	"testing"
)

func TestPruneToolResultSoftAndHardThresholds(t *testing.T) {
	cfg := DefaultPruningConfig()
	large := strings.Repeat("x", 5000)
	if got := pruneToolResult(large, 2, cfg); got != large {
		t.Fatal("recent result was pruned")
	}
	soft := pruneToolResult(large, 3, cfg)
	if len([]rune(soft)) >= len([]rune(large)) || !strings.Contains(soft, "2000 characters pruned") {
		t.Fatalf("unexpected soft prune: length=%d", len([]rune(soft)))
	}
	hard := pruneToolResult("small", 10, cfg)
	if !strings.Contains(hard, "cleared after 10 turns") {
		t.Fatalf("unexpected hard clear: %q", hard)
	}
}
