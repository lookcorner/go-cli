package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type defaultTypeBackend struct{ got SubagentRequest }

func (*defaultTypeBackend) Description() string   { return "default type fixture" }
func (b *defaultTypeBackend) DefaultType() string { return "explore" }
func (b *defaultTypeBackend) Start(_ context.Context, request SubagentRequest) (SubagentResult, error) {
	b.got = request
	return SubagentResult{ID: "fixture", Type: request.Type, Status: "completed"}, nil
}
func (*defaultTypeBackend) Has(string) bool { return true }
func (*defaultTypeBackend) Output(context.Context, string, time.Duration) (SubagentResult, error) {
	return SubagentResult{}, nil
}
func (*defaultTypeBackend) Kill(context.Context, string) (string, error) { return "", nil }

func TestTaskUsesBackendDefaultAgentWhenTypeOmitted(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, nil)
	defer registry.Close()
	backend := &defaultTypeBackend{}
	if err := registry.SetSubagentBackend(backend); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "task", json.RawMessage(`{"prompt":"inspect","description":"inspect"}`)); err != nil {
		t.Fatal(err)
	}
	if backend.got.Type != "explore" {
		t.Fatalf("type=%q", backend.got.Type)
	}
}
