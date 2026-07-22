package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseClientFS(t *testing.T) {
	tests := []struct {
		name     string
		params   string
		enabled  bool
		mode     clientFSMode
		debounce time.Duration
		ignore   []string
	}{
		{"missing", `{}`, false, clientFSEvents, 0, nil},
		{"false", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":false}}}`, false, clientFSEvents, 0, nil},
		{"true", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":true}}}`, true, clientFSEvents, 100 * time.Millisecond, nil},
		{"object defaults", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{}}}}`, true, clientFSEvents, 100 * time.Millisecond, nil},
		{"disabled object", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{"enabled":false}}}}`, false, clientFSEvents, 0, nil},
		{"index options", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{"index":true,"debounce_ms":25,"ignore":["*.tmp",7,"!keep.tmp"]}}}}`, true, clientFSIndex, 25 * time.Millisecond, []string{"*.tmp", "!keep.tmp"}},
		{"negative debounce", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{"debounce_ms":-1}}}}`, true, clientFSEvents, 100 * time.Millisecond, nil},
		{"fractional debounce", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{"debounce_ms":1.5}}}}`, true, clientFSEvents, 100 * time.Millisecond, nil},
		{"overflow debounce", `{"clientCapabilities":{"_meta":{"x.ai/fs_notify":{"debounce_ms":1e30}}}}`, true, clientFSEvents, 100 * time.Millisecond, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := parseClientFS([]byte(test.params))
			if (config != nil) != test.enabled {
				t.Fatalf("config=%#v enabled=%v", config, test.enabled)
			}
			if config != nil && (config.mode != test.mode || config.debounce != test.debounce || !reflect.DeepEqual(config.ignore, test.ignore)) {
				t.Fatalf("config=%#v", config)
			}
		})
	}
}

func TestInitialFileIndexChunks(t *testing.T) {
	empty, err := workspace.BuildFileIndex(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyOutput := &synchronizedBuffer{}
	(&Server{output: emptyOutput}).sendInitialFileIndex(context.Background(), "empty", empty)
	if messages := decodeACPBytes(t, emptyOutput.snapshot()); len(messages) != 0 {
		t.Fatalf("empty index messages=%#v", messages)
	}

	root := t.TempDir()
	for index := 0; index < 501; index++ {
		path := filepath.Join(root, fmt.Sprintf("file-%03d", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := workspace.BuildFileIndex(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := &synchronizedBuffer{}
	server := &Server{output: output}
	server.sendInitialFileIndex(context.Background(), "chunked", index)
	messages := decodeACPBytes(t, output.snapshot())
	if len(messages) != 2 {
		t.Fatalf("messages=%d", len(messages))
	}
	for chunk, message := range messages {
		params := message["params"].(map[string]any)
		files := params["files"].([]any)
		wantFiles := 500
		if chunk == 1 {
			wantFiles = 1
		}
		if message["method"] != "x.ai/fs/index" || params["sessionId"] != "chunked" || params["root"] != index.Root() || params["chunk"] != float64(chunk) || params["totalChunks"] != float64(2) || params["totalFiles"] != float64(501) || params["complete"] != (chunk == 1) || len(files) != wantFiles {
			t.Fatalf("chunk %d: %#v", chunk, params)
		}
	}
}

func TestFileChangeWireModes(t *testing.T) {
	root := t.TempDir()
	changes := []workspace.FileChange{
		{Kind: workspace.FileCreated, Entries: []workspace.FileIndexEntry{{Path: "new.txt"}}},
		{Kind: workspace.FileModified, Entries: []workspace.FileIndexEntry{{Path: "changed.txt"}}},
		{Kind: workspace.FileRemoved, Entries: []workspace.FileIndexEntry{{Path: "old.txt"}}},
	}

	eventsOutput := &synchronizedBuffer{}
	(&Server{output: eventsOutput}).sendFileChanges(context.Background(), "events", root, clientFSEvents, changes)
	events := decodeACPBytes(t, eventsOutput.snapshot())
	if len(events) != 3 {
		t.Fatalf("events=%#v", events)
	}
	for index, kind := range []string{"Create", "Modify", "Remove"} {
		params := events[index]["params"].(map[string]any)
		event := params["event"].(map[string]any)
		path := event["paths"].([]any)[0].(string)
		if events[index]["method"] != "x.ai/fs_notify" || params["sessionId"] != "events" || event["kind"] != kind || !filepath.IsAbs(path) {
			t.Fatalf("event %d: %#v", index, events[index])
		}
	}

	indexOutput := &synchronizedBuffer{}
	(&Server{output: indexOutput}).sendFileChanges(context.Background(), "index", root, clientFSIndex, changes)
	deltas := decodeACPBytes(t, indexOutput.snapshot())
	if len(deltas) != 2 {
		t.Fatalf("deltas=%#v", deltas)
	}
	add := deltas[0]["params"].(map[string]any)["delta"].(map[string]any)
	remove := deltas[1]["params"].(map[string]any)["delta"].(map[string]any)
	if add["op"] != "add" || add["entries"].([]any)[0].(map[string]any)["path"] != "new.txt" || remove["op"] != "remove" || remove["paths"].([]any)[0] != "old.txt" {
		t.Fatalf("deltas=%#v", deltas)
	}
}

func TestFileWatcherStopsWithSession(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "initial.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &synchronizedBuffer{}
	server := &Server{output: output, clientFS: &clientFSConfig{mode: clientFSIndex, debounce: 10 * time.Millisecond}}
	current := &session{id: "closing", cwd: root, ctx: ctx}
	server.startFileNotifications(current)
	waitForACPMessages(t, output, 1)
	server.shutdownSession(current)
	before := len(decodeACPBytes(t, output.snapshot()))
	if err := os.WriteFile(filepath.Join(root, "after-close.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if after := len(decodeACPBytes(t, output.snapshot())); after != before {
		t.Fatalf("messages after close=%d before=%d", after, before)
	}
}

func waitForACPMessages(t *testing.T, output *synchronizedBuffer, count int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		messages := decodeACPBytes(t, output.snapshot())
		if len(messages) >= count {
			return messages
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d ACP messages", count)
	return nil
}
