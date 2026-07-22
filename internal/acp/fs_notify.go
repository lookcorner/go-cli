package acp

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type clientFSMode uint8

const (
	clientFSEvents clientFSMode = iota
	clientFSIndex
	fileIndexChunkSize = 500
)

type clientFSConfig struct {
	mode     clientFSMode
	debounce time.Duration
	ignore   []string
}

func parseClientFS(raw json.RawMessage) *clientFSConfig {
	var params struct {
		ClientCapabilities struct {
			Meta map[string]any `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return nil
	}
	value, exists := params.ClientCapabilities.Meta["x.ai/fs_notify"]
	if !exists {
		return nil
	}
	config := &clientFSConfig{debounce: 100 * time.Millisecond}
	if enabled, ok := value.(bool); ok {
		if !enabled {
			return nil
		}
		return config
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if enabled, ok := object["enabled"].(bool); ok && !enabled {
		return nil
	}
	if indexed, _ := object["index"].(bool); indexed {
		config.mode = clientFSIndex
	}
	if millis, ok := object["debounce_ms"].(float64); ok && millis >= 0 && math.Trunc(millis) == millis && millis <= float64(math.MaxInt64/int64(time.Millisecond)) {
		config.debounce = time.Duration(millis) * time.Millisecond
	}
	if values, ok := object["ignore"].([]any); ok {
		for _, value := range values {
			if pattern, ok := value.(string); ok {
				config.ignore = append(config.ignore, pattern)
			}
		}
	}
	return config
}

func (s *Server) startFileNotifications(current *session) {
	if s.clientFS == nil {
		return
	}
	config := *s.clientFS
	config.ignore = append([]string(nil), config.ignore...)
	ctx, cancel := context.WithCancel(current.ctx)
	done := make(chan struct{})
	current.mu.Lock()
	current.fileWatchCancel, current.fileWatchDone = cancel, done
	current.mu.Unlock()
	go func() {
		defer close(done)
		index, err := workspace.BuildFileIndex(ctx, current.cwd, config.ignore)
		if err != nil {
			return
		}
		_ = workspace.WatchFileIndex(ctx, index, config.debounce, config.ignore, func() {
			if config.mode == clientFSIndex {
				s.sendInitialFileIndex(ctx, current.id, index)
			}
		}, func(changes []workspace.FileChange) {
			s.sendFileChanges(ctx, current.id, index.Root(), config.mode, changes)
		})
	}()
}

func (s *Server) sendInitialFileIndex(ctx context.Context, sessionID string, index workspace.FileIndex) {
	entries := index.Entries()
	totalChunks := (len(entries) + fileIndexChunkSize - 1) / fileIndexChunkSize
	for chunk := 0; chunk < totalChunks; chunk++ {
		if ctx.Err() != nil {
			return
		}
		start := chunk * fileIndexChunkSize
		end := min(start+fileIndexChunkSize, len(entries))
		s.write(map[string]any{
			"jsonrpc": "2.0", "method": "x.ai/fs/index",
			"params": map[string]any{
				"sessionId": sessionID, "root": index.Root(), "files": entries[start:end],
				"chunk": chunk, "totalChunks": totalChunks, "totalFiles": len(entries), "complete": chunk == totalChunks-1,
			},
		})
	}
}

func (s *Server) sendFileChanges(ctx context.Context, sessionID, root string, mode clientFSMode, changes []workspace.FileChange) {
	for _, change := range changes {
		if ctx.Err() != nil {
			return
		}
		if mode == clientFSEvents {
			kind := map[workspace.FileChangeKind]string{
				workspace.FileCreated: "Create", workspace.FileModified: "Modify", workspace.FileRemoved: "Remove",
			}[change.Kind]
			paths := make([]string, len(change.Entries))
			for index, entry := range change.Entries {
				paths[index] = filepath.Join(root, filepath.FromSlash(entry.Path))
			}
			s.write(map[string]any{
				"jsonrpc": "2.0", "method": "x.ai/fs_notify",
				"params": map[string]any{"sessionId": sessionID, "event": map[string]any{"kind": kind, "paths": paths}},
			})
			continue
		}
		switch change.Kind {
		case workspace.FileCreated:
			s.write(map[string]any{
				"jsonrpc": "2.0", "method": "x.ai/fs/index/delta",
				"params": map[string]any{"sessionId": sessionID, "delta": map[string]any{"op": "add", "entries": change.Entries}},
			})
		case workspace.FileRemoved:
			paths := make([]string, len(change.Entries))
			for index, entry := range change.Entries {
				paths[index] = entry.Path
			}
			s.write(map[string]any{
				"jsonrpc": "2.0", "method": "x.ai/fs/index/delta",
				"params": map[string]any{"sessionId": sessionID, "delta": map[string]any{"op": "remove", "paths": paths}},
			})
		}
	}
}
