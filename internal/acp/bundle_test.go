package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/lookcorner/go-cli/internal/bundle"
)

func TestBundleExtensions(t *testing.T) {
	version := "v1"
	var output bytes.Buffer
	server := &Server{output: &output, Bundle: BundleConfig{
		Status: func() (bundle.Status, error) {
			return bundle.Status{HasCache: true, Version: &version, Agents: []string{"worker"}, Personas: []string{}, Roles: []string{}, Skills: []string{}}, nil
		},
		Entry: func(kind, name string) (bundle.Entry, error) {
			return bundle.Entry{Kind: kind, Name: name, Content: "# Worker"}, nil
		},
		Sync: func(_ context.Context, force bool) (bundle.SyncResult, error) {
			return bundle.SyncResult{Updated: force, Version: "v2", AgentsCount: 1}, nil
		},
	}}

	server.handleBundle(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/bundle/status"})
	server.handleBundle(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/bundle/entry/get", Params: json.RawMessage(`{"kind":"agent","name":"worker"}`)})
	server.handleBundle(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/bundle/sync", Params: json.RawMessage(`{"force":true}`)})
	decoder := json.NewDecoder(&output)
	status := decodeACP(t, decoder)["result"].(map[string]any)
	entry := decodeACP(t, decoder)["result"].(map[string]any)
	synced := decodeACP(t, decoder)["result"].(map[string]any)
	if status["hasCache"] != true || status["version"] != "v1" || status["agents"].([]any)[0] != "worker" {
		t.Fatalf("status=%#v", status)
	}
	if entry["kind"] != "agent" || entry["name"] != "worker" || entry["content"] != "# Worker" {
		t.Fatalf("entry=%#v", entry)
	}
	if synced["updated"] != true || synced["version"] != "v2" || synced["agentsCount"] != float64(1) {
		t.Fatalf("synced=%#v", synced)
	}
}

func TestBundleExtensionsValidateParameters(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, Bundle: BundleConfig{Status: func() (bundle.Status, error) { return bundle.Status{}, nil }}}
	server.handleBundle(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/bundle/status", Params: json.RawMessage(`[]`)})
	server.handleBundle(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/bundle/entry/get", Params: json.RawMessage(`{"kind":"agent"}`)})
	server.handleBundle(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/bundle/sync", Params: json.RawMessage(`{`)})
	messages := decodeACPOutput(t, output.Bytes())
	for index, code := range []float64{-32602, -32000, -32000} {
		if messages[index]["error"].(map[string]any)["code"] != code {
			t.Fatalf("messages=%#v", messages)
		}
	}
}
