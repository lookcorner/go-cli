package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxMemoryFileBytes = 1 << 20
	maxContextResults  = 6
	maxSnippetChars    = 500
)

var globalAppendMu sync.Mutex

type Store struct {
	mu           sync.Mutex
	root         string
	workspaceDir string
	sessionsDir  string
	sessionID    string
}

type FileInfo struct {
	Path                 string  `json:"path"`
	Source               string  `json:"source"`
	SizeBytes            uint64  `json:"size_bytes"`
	ModifiedEpochSeconds *uint64 `json:"modified_epoch_secs,omitempty"`
}

func DefaultRoot() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, "memory"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok", "memory"), nil
}

func WorkspacePath(root, workspace string) (string, error) {
	absWorkspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(absWorkspace))
	return filepath.Join(filepath.Clean(root), hex.EncodeToString(digest[:8])), nil
}

func GlobalPath(root string) string { return filepath.Join(filepath.Clean(root), "MEMORY.md") }

func ClearWorkspace(root, workspace string) (bool, error) {
	path, err := WorkspacePath(root, workspace)
	if err != nil {
		return false, err
	}
	return removeMemoryPath(path, true)
}

func ClearGlobal(root string) (bool, error) { return removeMemoryPath(GlobalPath(root), false) }

func removeMemoryPath(path string, directory bool) (bool, error) {
	parent, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() {
		return false, errors.New("memory root has an unsafe file type")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() {
		return false, errors.New("memory path has an unsafe file type")
	}
	if directory {
		return true, os.RemoveAll(path)
	}
	return true, os.Remove(path)
}

func AppendGlobal(root, content string) (string, error) {
	content = normalizeMemoryContent(content)
	if content == "" {
		return "", errors.New("memory note is empty")
	}
	globalAppendMu.Lock()
	defer globalAppendMu.Unlock()
	if err := ensureDirectory(root); err != nil {
		return "", err
	}
	path := filepath.Join(root, "MEMORY.md")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxMemoryFileBytes {
			return "", errors.New("global memory must be a bounded regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	prefix := ""
	info, statErr := file.Stat()
	if statErr != nil {
		return "", statErr
	}
	if info.Size() > 0 {
		prefix = "\n\n"
	}
	if info.Size()+int64(len(prefix)+len(content)) > maxMemoryFileBytes {
		return "", errors.New("global memory would exceed the file size limit")
	}
	if _, err := file.WriteString(prefix + content); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

func normalizeMemoryContent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "#") {
		return value
	}
	first, rest, found := strings.Cut(value, "\n")
	if !found {
		return "## " + value
	}
	first, rest = strings.TrimSpace(first), strings.TrimSpace(rest)
	if len(first) <= 80 {
		return "## " + first + "\n\n" + rest
	}
	return "## Note\n\n" + value
}

func Open(root, workspace, sessionID string) (*Store, error) {
	root, workspace = filepath.Clean(root), filepath.Clean(workspace)
	if root == "." || workspace == "." || strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("memory root, workspace, and session ID are required")
	}
	workspaceDir, err := WorkspacePath(root, workspace)
	if err != nil {
		return nil, err
	}
	store := &Store{
		root: root, workspaceDir: workspaceDir, sessionsDir: filepath.Join(workspaceDir, "sessions"),
		sessionID: safeName(sessionID),
	}
	for _, dir := range []string{store.root, store.workspaceDir, store.sessionsDir} {
		if err := ensureDirectory(dir); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Write(trigger, content string) (string, bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false, errors.New("memory content is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureDirectory(s.sessionsDir); err != nil {
		return "", false, err
	}
	entries, err := sessionEntries(s.sessionsDir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		data, readErr := readMemoryFile(entry.path)
		if readErr == nil && strings.TrimSpace(string(data)) == content {
			return entry.path, false, nil
		}
	}
	now := time.Now().UTC()
	name := fmt.Sprintf("%s-%s-%s-%d.md", now.Format("2006-01-02"), safeName(trigger), s.sessionID, now.UnixNano())
	path := filepath.Join(s.sessionsDir, name)
	tempPath := filepath.Join(s.sessionsDir, ".tmp-"+name)
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.WriteString(content + "\n"); err != nil {
		return "", false, err
	}
	if err := file.Sync(); err != nil {
		return "", false, err
	}
	if err := file.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", false, err
	}
	remove = false
	return path, true, nil
}

func (s *Store) List() ([]FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureDirectory(s.sessionsDir); err != nil {
		return nil, err
	}
	files := make([]FileInfo, 0)
	for _, candidate := range []struct {
		path   string
		source string
	}{
		{filepath.Join(s.root, "MEMORY.md"), "global"},
		{filepath.Join(s.workspaceDir, "MEMORY.md"), "workspace"},
	} {
		if info, ok := memoryFileInfo(candidate.path, candidate.source); ok {
			files = append(files, info)
		}
	}
	entries, err := sessionEntries(s.sessionsDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if info, ok := memoryFileInfo(entry.path, "session"); ok {
			files = append(files, info)
		}
	}
	return files, nil
}

func (s *Store) Context() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureDirectory(s.sessionsDir); err != nil {
		return "", err
	}
	type candidate struct {
		path   string
		source string
	}
	candidates := []candidate{
		{path: filepath.Join(s.workspaceDir, "MEMORY.md"), source: "workspace"},
		{path: filepath.Join(s.root, "MEMORY.md"), source: "global"},
	}
	entries, err := sessionEntries(s.sessionsDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		candidates = append(candidates, candidate{path: entry.path, source: "session"})
	}
	var output strings.Builder
	result := 0
	for _, candidate := range candidates {
		if result >= maxContextResults {
			break
		}
		data, err := readMemoryFile(candidate.path)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		if result == 0 {
			output.WriteString("<memory-context>\n## Relevant Memory from Past Sessions\n\n")
		}
		result++
		snippet := []rune(strings.TrimSpace(string(data)))
		truncated := len(snippet) > maxSnippetChars
		if truncated {
			snippet = snippet[:maxSnippetChars]
		}
		path, _ := filepath.Rel(s.root, candidate.path)
		fmt.Fprintf(&output, "### Result %d (source: %s)\n**File:** %s\n```\n%s", result, candidate.source, filepath.ToSlash(path), string(snippet))
		if truncated {
			output.WriteString("...")
		}
		output.WriteString("\n```\n\n")
	}
	if result == 0 {
		return "", nil
	}
	output.WriteString("</memory-context>")
	return output.String(), nil
}

type memoryEntry struct {
	path     string
	modified time.Time
}

func sessionEntries(dir string) ([]memoryEntry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]memoryEntry, 0, len(items))
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 || item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".md") {
			continue
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxMemoryFileBytes {
			continue
		}
		entries = append(entries, memoryEntry{path: filepath.Join(dir, item.Name()), modified: info.ModTime()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modified.Equal(entries[j].modified) {
			return entries[i].path > entries[j].path
		}
		return entries[i].modified.After(entries[j].modified)
	})
	return entries, nil
}

func readMemoryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxMemoryFileBytes {
		return nil, errors.New("memory source must be a bounded regular file")
	}
	return os.ReadFile(path)
}

func memoryFileInfo(path, source string) (FileInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxMemoryFileBytes {
		return FileInfo{}, false
	}
	var modified *uint64
	if seconds := info.ModTime().Unix(); seconds > 0 {
		value := uint64(seconds)
		modified = &value
	}
	return FileInfo{Path: path, Source: source, SizeBytes: uint64(info.Size()), ModifiedEpochSeconds: modified}, true
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("memory path must be a directory, not a symlink")
	}
	return nil
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(char rune) rune {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
			return char
		default:
			return '-'
		}
	}, value)
}
