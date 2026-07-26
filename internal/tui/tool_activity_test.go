package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
)

func TestToolActivityLabelPrefersDescription(t *testing.T) {
	call := api.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"cargo test","description":"run tests"}`),
	}
	if got := toolActivityLabel(call); got != "run tests…" {
		t.Fatalf("got=%q", got)
	}
}

func TestToolActivityLabelUsesCommandOrName(t *testing.T) {
	if got := toolActivityLabel(api.ToolCall{
		Name: "shell", Arguments: json.RawMessage(`{"command":"cargo build"}`),
	}); got != "Running: cargo build" {
		t.Fatalf("command=%q", got)
	}
	if got := toolActivityLabel(api.ToolCall{Name: "read_file"}); got != "Running: read_file" {
		t.Fatalf("name=%q", got)
	}
	if got := toolActivityLabel(api.ToolCall{}); got != "Running tool" {
		t.Fatalf("empty=%q", got)
	}
}

func TestToolActivityLabelTruncatesLongTitles(t *testing.T) {
	long := strings.Repeat("a", 40)
	got := toolActivityLabel(api.ToolCall{
		Name: "shell", Arguments: json.RawMessage(`{"command":` + jsonString(long) + `}`),
	})
	want := "Running: " + strings.Repeat("a", 30) + "…"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func jsonString(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}
