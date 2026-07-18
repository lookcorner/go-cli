package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxHunkStateBytes = 8 << 20

type hunkStateFile struct {
	Version     int                             `json:"version"`
	Head        string                          `json:"head,omitempty"`
	HeadLogSize int64                           `json:"headLogSize,omitempty"`
	AgentHunks  map[string]hunkAttributionState `json:"agentHunks"`
	AgentFiles  []string                        `json:"agentFiles,omitempty"`
	Accepted    []string                        `json:"accepted"`
	Stats       HunkSessionStats                `json:"stats"`
}

type hunkAttributionState struct {
	CreatedAt   time.Time `json:"createdAt"`
	PromptIndex *int      `json:"promptIndex,omitempty"`
}

func (t *HunkTracker) configureState(path string) error {
	if t == nil || path == "" {
		return errors.New("hunk tracker state path is required")
	}
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := readHunkState(path)
	if errors.Is(err, os.ErrNotExist) {
		t.statePath = path
		return nil
	}
	if err != nil {
		return err
	}
	var stored hunkStateFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode hunk tracker state: %w", err)
	}
	if stored.Version != 1 {
		return fmt.Errorf("unsupported hunk tracker state version %d", stored.Version)
	}
	t.agentHunks = make(map[string]hunkAttribution, len(stored.AgentHunks))
	for key, item := range stored.AgentHunks {
		attribution := hunkAttribution{createdAt: item.CreatedAt}
		if item.PromptIndex != nil {
			attribution.promptIndex = *item.PromptIndex
			attribution.hasPromptIndex = true
		}
		t.agentHunks[key] = attribution
	}
	t.agentFiles = make(map[string]bool, len(stored.AgentFiles))
	for _, path := range stored.AgentFiles {
		if path != "" {
			t.agentFiles[path] = true
		}
	}
	t.accepted = make(map[string]bool, len(stored.Accepted))
	for _, id := range stored.Accepted {
		if id != "" {
			t.accepted[id] = true
		}
	}
	t.stats = stored.Stats
	currentHead, currentHeadLogSize := t.currentGitIdentity(context.Background())
	if currentHead != "" && gitIdentityChanged(stored.Head, stored.HeadLogSize, currentHead, currentHeadLogSize) {
		t.agentHunks = make(map[string]hunkAttribution)
		t.accepted = make(map[string]bool)
	}
	t.head = currentHead
	t.headLogSize = currentHeadLogSize
	if t.head == "" {
		t.head = stored.Head
		t.headLogSize = stored.HeadLogSize
	}
	t.statePath = path
	return nil
}

func (t *HunkTracker) saveState() error {
	if t == nil {
		return nil
	}
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	t.mu.RLock()
	path := t.statePath
	stored := hunkStateFile{
		Version: 1, Head: t.head, HeadLogSize: t.headLogSize, AgentHunks: make(map[string]hunkAttributionState, len(t.agentHunks)),
		AgentFiles: make([]string, 0, len(t.agentFiles)), Accepted: make([]string, 0, len(t.accepted)), Stats: t.stats,
	}
	for key, item := range t.agentHunks {
		state := hunkAttributionState{CreatedAt: item.createdAt}
		if item.hasPromptIndex {
			index := item.promptIndex
			state.PromptIndex = &index
		}
		stored.AgentHunks[key] = state
	}
	for id := range t.accepted {
		stored.Accepted = append(stored.Accepted, id)
	}
	for path := range t.agentFiles {
		stored.AgentFiles = append(stored.AgentFiles, path)
	}
	t.mu.RUnlock()
	if path == "" {
		return nil
	}
	sort.Strings(stored.Accepted)
	sort.Strings(stored.AgentFiles)
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxHunkStateBytes {
		return fmt.Errorf("hunk tracker state exceeds %d bytes", maxHunkStateBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("hunk tracker state must be a regular, non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".gork-hunks-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tempPath, path)
	}
	return err
}

func readHunkState(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	linked, linkErr := os.Lstat(path)
	if err != nil || linkErr != nil {
		return nil, errors.New("validate hunk tracker state file")
	}
	if linked.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return nil, errors.New("hunk tracker state must be a regular, non-symlink file")
	}
	if opened.Size() > maxHunkStateBytes {
		return nil, fmt.Errorf("hunk tracker state exceeds %d bytes", maxHunkStateBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHunkStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxHunkStateBytes {
		return nil, fmt.Errorf("hunk tracker state exceeds %d bytes", maxHunkStateBytes)
	}
	return data, nil
}
