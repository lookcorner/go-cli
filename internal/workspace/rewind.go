package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxRewindFileBytes = 8 << 20
	maxRewindLogBytes  = 256 << 20
)

type RewindConflict struct {
	Path         string `json:"path"`
	ConflictType string `json:"conflict_type"`
}

type FileRewindPreview struct {
	CleanFiles []string
	Conflicts  []RewindConflict
}

type RewindStore struct {
	mu       sync.Mutex
	ws       *Workspace
	path     string
	captured map[string]bool
}

type WorkspaceCheckpoint struct {
	promptIndex int
	files       map[string]fileState
}

type fileState struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type rewindEvent struct {
	Time        time.Time `json:"time"`
	Kind        string    `json:"kind"`
	PromptIndex int       `json:"prompt_index"`
	Path        string    `json:"path,omitempty"`
	State       fileState `json:"state,omitempty"`
}

type rewindFilePlan struct {
	before fileState
	after  *fileState
}

func NewRewindStore(ws *Workspace, path string) (*RewindStore, error) {
	if ws == nil || path == "" {
		return nil, errors.New("workspace and rewind path are required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create rewind directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure rewind directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("rewind store must be a regular, non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create rewind store: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure rewind store: %w", err)
	}
	store := &RewindStore{ws: ws, path: path, captured: make(map[string]bool)}
	events, err := store.loadLocked()
	if err != nil {
		return nil, err
	}
	store.rebuildCaptured(events)
	return store, nil
}

func (s *RewindStore) CaptureBefore(promptIndex int, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, state, err := s.snapshot(path)
	if err != nil {
		return err
	}
	key := rewindKey(promptIndex, rel)
	if s.captured[key] {
		return nil
	}
	if err := s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "before", PromptIndex: promptIndex, Path: rel, State: state}); err != nil {
		return err
	}
	s.captured[key] = true
	return nil
}

func (s *RewindStore) CaptureAfter(promptIndex int, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, state, err := s.snapshot(path)
	if err != nil {
		return err
	}
	if !s.captured[rewindKey(promptIndex, rel)] {
		return errors.New("rewind before-snapshot is missing")
	}
	return s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "after", PromptIndex: promptIndex, Path: rel, State: state})
}

// CaptureWorkspaceBefore holds the rewindable workspace state in memory until
// a shell command finishes. Only changed files are written to the rewind log.
func (s *RewindStore) CaptureWorkspaceBefore(promptIndex int) (*WorkspaceCheckpoint, error) {
	if promptIndex < 0 {
		return nil, errors.New("file mutation has no active prompt checkpoint")
	}
	files, err := s.workspaceFiles()
	if err != nil {
		return nil, err
	}
	return &WorkspaceCheckpoint{promptIndex: promptIndex, files: files}, nil
}

func (s *RewindStore) CaptureWorkspaceAfter(checkpoint *WorkspaceCheckpoint) error {
	if checkpoint == nil {
		return nil
	}
	after, err := s.workspaceFiles()
	if err != nil {
		return err
	}
	paths := make(map[string]bool, len(checkpoint.files)+len(after))
	for path := range checkpoint.files {
		paths[path] = true
	}
	for path := range after {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, path := range ordered {
		beforeState, beforeExists := checkpoint.files[path]
		afterState, afterExists := after[path]
		if !beforeExists {
			beforeState = fileState{}
		}
		if !afterExists {
			afterState = fileState{}
		}
		if sameFileState(beforeState, afterState) {
			continue
		}
		key := rewindKey(checkpoint.promptIndex, path)
		if !s.captured[key] {
			if err := s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "before", PromptIndex: checkpoint.promptIndex, Path: path, State: beforeState}); err != nil {
				return err
			}
			s.captured[key] = true
		}
		if err := s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "after", PromptIndex: checkpoint.promptIndex, Path: path, State: afterState}); err != nil {
			return err
		}
	}
	return nil
}

func (s *RewindStore) Cancel(promptIndex int, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resolved, err := s.ws.Resolve(path)
	if err != nil {
		return err
	}
	rel := s.ws.Relative(resolved)
	key := rewindKey(promptIndex, rel)
	if !s.captured[key] {
		return nil
	}
	if err := s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "cancel", PromptIndex: promptIndex, Path: rel}); err != nil {
		return err
	}
	delete(s.captured, key)
	return nil
}

func (s *RewindStore) Counts() (map[int]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	paths := make(map[int]map[string]bool)
	for _, event := range events {
		if event.Kind != "before" {
			continue
		}
		if paths[event.PromptIndex] == nil {
			paths[event.PromptIndex] = make(map[string]bool)
		}
		paths[event.PromptIndex][event.Path] = true
	}
	counts := make(map[int]int, len(paths))
	for index, items := range paths {
		counts[index] = len(items)
	}
	return counts, nil
}

func (s *RewindStore) Preview(target int) (FileRewindPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, preview, err := s.planLocked(target)
	return preview, err
}

func (s *RewindStore) Restore(target int) ([]string, FileRewindPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, preview, err := s.planLocked(target)
	if err != nil {
		return nil, preview, err
	}
	paths := make([]string, 0, len(plan))
	for path := range plan {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	reverted := []string{}
	for _, path := range paths {
		if err := s.restore(path, plan[path].before); err != nil {
			return reverted, preview, fmt.Errorf("restore %s: %w", path, err)
		}
		reverted = append(reverted, path)
	}
	if err := s.appendLocked(rewindEvent{Time: time.Now().UTC(), Kind: "truncate", PromptIndex: target}); err != nil {
		return reverted, preview, err
	}
	events, err := s.loadLocked()
	if err == nil {
		s.rebuildCaptured(events)
	}
	return reverted, preview, err
}

func (s *RewindStore) planLocked(target int) (map[string]rewindFilePlan, FileRewindPreview, error) {
	if target < 0 {
		return nil, FileRewindPreview{}, errors.New("rewind target must not be negative")
	}
	events, err := s.loadLocked()
	if err != nil {
		return nil, FileRewindPreview{}, err
	}
	plan := make(map[string]rewindFilePlan)
	for _, event := range events {
		if event.PromptIndex < target || event.Path == "" {
			continue
		}
		item, exists := plan[event.Path]
		switch event.Kind {
		case "before":
			if !exists {
				item.before = event.State
			}
		case "after":
			if !exists {
				return nil, FileRewindPreview{}, fmt.Errorf("after-snapshot for %s has no before-snapshot", event.Path)
			}
			state := event.State
			item.after = &state
		}
		plan[event.Path] = item
	}
	preview := FileRewindPreview{CleanFiles: []string{}, Conflicts: []RewindConflict{}}
	for path, item := range plan {
		_, current, err := s.snapshot(path)
		if err != nil {
			return nil, preview, err
		}
		if item.after != nil && sameFileState(current, *item.after) {
			preview.CleanFiles = append(preview.CleanFiles, path)
			continue
		}
		kind := "modified_externally"
		if item.after != nil {
			if !current.Exists && item.after.Exists {
				kind = "deleted_externally"
			} else if current.Exists && !item.after.Exists {
				kind = "created_externally"
			}
		}
		preview.Conflicts = append(preview.Conflicts, RewindConflict{Path: path, ConflictType: kind})
	}
	sort.Strings(preview.CleanFiles)
	sort.Slice(preview.Conflicts, func(i, j int) bool { return preview.Conflicts[i].Path < preview.Conflicts[j].Path })
	return plan, preview, nil
}

func (s *RewindStore) snapshot(path string) (string, fileState, error) {
	resolved, err := s.ws.Resolve(path)
	if err != nil {
		return "", fileState{}, err
	}
	rel := s.ws.Relative(resolved)
	info, err := os.Lstat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return rel, fileState{}, nil
	}
	if err != nil {
		return "", fileState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxRewindFileBytes {
		return "", fileState{}, fmt.Errorf("rewind only supports regular files up to %d bytes", maxRewindFileBytes)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", fileState{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	linked, linkErr := os.Lstat(resolved)
	if err != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, linked) || opened.Size() > maxRewindFileBytes {
		return "", fileState{}, errors.New("file changed while creating rewind snapshot")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRewindFileBytes+1))
	if err != nil || len(data) > maxRewindFileBytes {
		return "", fileState{}, errors.New("read rewind snapshot failed or exceeded its size limit")
	}
	return rel, fileState{Exists: true, Data: data, Mode: uint32(opened.Mode().Perm())}, nil
}

func (s *RewindStore) workspaceFiles() (map[string]fileState, error) {
	files := make(map[string]fileState)
	var candidates []string
	err := filepath.WalkDir(s.ws.Root(), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == s.ws.Root() {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || IsGitIgnored(s.ws.Root(), path) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace: %w", err)
	}
	ignored := GitIgnored(s.ws.Root(), candidates)
	for _, path := range candidates {
		if ignored[path] {
			continue
		}
		rel, state, err := s.snapshot(path)
		if err != nil {
			return nil, fmt.Errorf("snapshot workspace: %w", err)
		}
		files[rel] = state
	}
	return files, nil
}

func (s *RewindStore) restore(path string, state fileState) error {
	resolved, err := s.ws.Resolve(path)
	if err != nil {
		return err
	}
	if !state.Exists {
		info, err := os.Lstat(resolved)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("refusing to remove a non-regular file")
		}
		return os.Remove(resolved)
	}
	mode := os.FileMode(state.Mode)
	if mode == 0 {
		mode = 0o644
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".gork-rewind-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(state.Data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tempPath, resolved)
	}
	return err
}

func (s *RewindStore) appendLocked(event rewindEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	info, err := validateRewindFile(file, s.path)
	if err != nil {
		_ = file.Close()
		return err
	}
	if info.Size()+int64(len(encoded)+1) > maxRewindLogBytes {
		_ = file.Close()
		return fmt.Errorf("rewind store exceeds %d bytes", maxRewindLogBytes)
	}
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *RewindStore) loadLocked() ([]rewindEvent, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := validateRewindFile(file, s.path)
	if err != nil || info.Size() > maxRewindLogBytes {
		return nil, errors.New("invalid rewind store")
	}
	var events []rewindEvent
	scanner := bufio.NewScanner(io.LimitReader(file, maxRewindLogBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	line := 0
	for scanner.Scan() {
		line++
		var event rewindEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse rewind line %d: %w", line, err)
		}
		if event.Kind == "truncate" {
			kept := events[:0]
			for _, previous := range events {
				if previous.PromptIndex < event.PromptIndex {
					kept = append(kept, previous)
				}
			}
			events = kept
			continue
		}
		if event.Kind == "cancel" {
			kept := events[:0]
			for _, previous := range events {
				if previous.PromptIndex != event.PromptIndex || previous.Path != event.Path {
					kept = append(kept, previous)
				}
			}
			events = kept
			continue
		}
		if (event.Kind != "before" && event.Kind != "after") || event.PromptIndex < 0 || event.Path == "" {
			return nil, fmt.Errorf("invalid rewind event on line %d", line)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *RewindStore) rebuildCaptured(events []rewindEvent) {
	s.captured = make(map[string]bool)
	for _, event := range events {
		if event.Kind == "before" {
			s.captured[rewindKey(event.PromptIndex, event.Path)] = true
		}
	}
}

func rewindKey(index int, path string) string { return fmt.Sprintf("%d\x00%s", index, path) }

func sameFileState(first, second fileState) bool {
	return first.Exists == second.Exists && first.Mode == second.Mode && bytes.Equal(first.Data, second.Data)
}

func validateRewindFile(file *os.File, path string) (os.FileInfo, error) {
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	linked, err := os.Lstat(path)
	if err != nil || linked.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return nil, errors.New("rewind store changed while open")
	}
	return opened, nil
}
