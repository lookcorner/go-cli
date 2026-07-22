package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar"
	"github.com/fsnotify/fsnotify"
)

type FileIndexEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type FileChangeKind string

const (
	FileCreated  FileChangeKind = "created"
	FileModified FileChangeKind = "modified"
	FileRemoved  FileChangeKind = "removed"
)

type FileChange struct {
	Kind    FileChangeKind
	Entries []FileIndexEntry
}

type fileIndexState struct {
	entry   FileIndexEntry
	absPath string
	size    int64
	mode    fs.FileMode
	modTime int64
}

type FileIndex struct {
	root  string
	items map[string]fileIndexState
}

func BuildFileIndex(ctx context.Context, root string, ignorePatterns []string) (FileIndex, error) {
	workspace, err := Open(root)
	if err != nil {
		return FileIndex{}, err
	}
	root = workspace.Root()
	items := make(map[string]fileIndexState)
	var candidates []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() && (name == ".git" || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		if strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if customIgnored(rel, ignorePatterns) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		items[rel] = fileIndexState{
			entry: FileIndexEntry{Path: rel, IsDir: info.IsDir()}, absPath: path,
			size: info.Size(), mode: info.Mode(), modTime: info.ModTime().UnixNano(),
		}
		candidates = append(candidates, path)
		return nil
	})
	if err != nil {
		return FileIndex{}, err
	}
	for path := range GitIgnored(root, candidates) {
		if rel, relErr := filepath.Rel(root, path); relErr == nil {
			delete(items, filepath.ToSlash(rel))
		}
	}
	return FileIndex{root: root, items: items}, nil
}

func (i FileIndex) Root() string { return i.root }

func (i FileIndex) Entries() []FileIndexEntry {
	entries := make([]FileIndexEntry, 0, len(i.items))
	for _, item := range i.items {
		entries = append(entries, item.entry)
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Path < entries[b].Path })
	return entries
}

func WatchFileIndex(ctx context.Context, initial FileIndex, debounce time.Duration, ignorePatterns []string, ready func(), emit func([]FileChange)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if debounce < 0 {
		debounce = 0
	}
	watched := make(map[string]bool)
	reconcileFileIndexWatches(watcher, watched, initial)
	if ready != nil {
		ready()
	}
	current := initial
	var timer *time.Timer
	var timerC <-chan time.Time
	schedule := func() {
		if timer == nil {
			timer = time.NewTimer(debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounce)
		}
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				schedule()
			}
		case <-timerC:
			timerC = nil
			next, buildErr := BuildFileIndex(ctx, current.root, ignorePatterns)
			if buildErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				continue
			}
			reconcileFileIndexWatches(watcher, watched, next)
			changes := diffFileIndexes(current, next)
			current = next
			if len(changes) > 0 && emit != nil {
				emit(changes)
			}
		}
	}
}

func customIgnored(path string, patterns []string) bool {
	ignored := false
	for _, raw := range patterns {
		pattern := filepath.ToSlash(strings.TrimSpace(raw))
		if pattern == "" || pattern == "!" {
			continue
		}
		include := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		matched, _ := doublestar.Match(pattern, path)
		if !matched && !strings.Contains(pattern, "/") {
			matched, _ = doublestar.Match(pattern, filepath.Base(path))
		}
		if matched {
			ignored = !include
		}
	}
	return ignored
}

func reconcileFileIndexWatches(watcher *fsnotify.Watcher, watched map[string]bool, index FileIndex) {
	directories := map[string]bool{index.root: true}
	for _, item := range index.items {
		if item.entry.IsDir {
			directories[item.absPath] = true
		}
	}
	for path := range watched {
		if !directories[path] {
			_ = watcher.Remove(path)
			delete(watched, path)
		}
	}
	for path := range directories {
		if !watched[path] && watcher.Add(path) == nil {
			watched[path] = true
		}
	}
}

func diffFileIndexes(previous, next FileIndex) []FileChange {
	created, modified, removed := []FileIndexEntry{}, []FileIndexEntry{}, []FileIndexEntry{}
	for path, item := range next.items {
		before, ok := previous.items[path]
		if !ok {
			created = append(created, item.entry)
		} else if before.size != item.size || before.mode != item.mode || before.modTime != item.modTime {
			modified = append(modified, item.entry)
		}
	}
	for path, item := range previous.items {
		if _, ok := next.items[path]; !ok {
			removed = append(removed, item.entry)
		}
	}
	sortEntries := func(entries []FileIndexEntry) {
		sort.Slice(entries, func(a, b int) bool { return entries[a].Path < entries[b].Path })
	}
	var changes []FileChange
	for _, group := range []struct {
		kind    FileChangeKind
		entries []FileIndexEntry
	}{{FileCreated, created}, {FileModified, modified}, {FileRemoved, removed}} {
		if len(group.entries) > 0 {
			sortEntries(group.entries)
			changes = append(changes, FileChange{Kind: group.kind, Entries: group.entries})
		}
	}
	return changes
}
