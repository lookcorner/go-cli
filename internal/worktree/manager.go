package worktree

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type CreateRequest struct {
	SessionID               string   `json:"sessionId"`
	SourcePath              string   `json:"sourcePath"`
	WorktreePath            string   `json:"worktreePath"`
	CopyMode                string   `json:"copyMode"`
	GitRef                  string   `json:"gitRef"`
	CopyIgnoredInBackground bool     `json:"copyIgnoredInBackground"`
	IgnoredSkipPatterns     []string `json:"ignoredSkipPatterns"`
	WorktreeType            string   `json:"worktreeType"`
	Label                   string   `json:"label"`
}

type RemoveRequest struct {
	WorktreePath string `json:"worktreePath"`
	IDOrPath     string `json:"idOrPath"`
	Force        bool   `json:"force"`
	DryRun       bool   `json:"dryRun"`
}

type Record struct {
	ID             string    `json:"id"`
	Path           string    `json:"path"`
	SourceRepo     string    `json:"sourceRepo"`
	RepoName       string    `json:"repoName"`
	Kind           string    `json:"kind"`
	CreationMode   string    `json:"creationMode"`
	GitRef         string    `json:"gitRef,omitempty"`
	HeadCommit     string    `json:"headCommit"`
	SessionID      string    `json:"sessionId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	LastAccessedAt time.Time `json:"lastAccessedAt"`
	Status         string    `json:"status"`
	Label          string    `json:"label"`
}

type Manager struct {
	mu      sync.Mutex
	path    string
	base    string
	records map[string]Record
}

func NewManager(stateDir string) (*Manager, error) {
	if stateDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolve worktree state directory: %w", err)
		}
		stateDir = filepath.Join(cache, "gork-go")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create worktree state directory: %w", err)
	}
	m := &Manager{
		path:    filepath.Join(stateDir, "worktrees.json"),
		base:    filepath.Join(stateDir, "worktrees"),
		records: make(map[string]Record),
	}
	data, err := os.ReadFile(m.path)
	if err == nil {
		var records []Record
		if json.Unmarshal(data, &records) != nil {
			return nil, fmt.Errorf("decode worktree registry %q", m.path)
		}
		for _, record := range records {
			m.records[record.ID] = record
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Create(ctx context.Context, req CreateRequest) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.SourcePath) == "" {
		return Record{}, false, errors.New("sessionId and sourcePath are required")
	}
	if req.CopyMode == "" {
		req.CopyMode = "dirty"
	}
	if req.CopyMode != "clean" && req.CopyMode != "dirty" {
		return Record{}, false, errors.New("copyMode must be clean or dirty")
	}
	if req.WorktreeType == "" {
		req.WorktreeType = "linked"
	}
	if req.WorktreeType != "linked" && req.WorktreeType != "standalone" && req.WorktreeType != "git" {
		return Record{}, false, errors.New("worktreeType must be linked, standalone, or git")
	}
	root, err := gitOutput(ctx, req.SourcePath, "rev-parse", "--show-toplevel")
	if err != nil {
		return Record{}, false, err
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return Record{}, false, err
	}
	ref := req.GitRef
	if ref == "" {
		ref = "HEAD"
	}
	commit, err := gitOutput(ctx, root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return Record{}, false, fmt.Errorf("resolve git ref %q: %w", ref, err)
	}
	commit = strings.TrimSpace(commit)
	label := sanitizeLabel(req.Label)
	if label == "" {
		label = time.Now().Format("2006-01-02") + "-" + randomHex(4)
	}
	dest := req.WorktreePath
	if dest == "" {
		dest = filepath.Join(m.base, sanitizeLabel(filepath.Base(root)), label)
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return Record{}, false, err
	}
	if existing := m.recordByPath(dest); existing != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			if existing.SourceRepo != root || existing.SessionID != req.SessionID {
				return Record{}, false, fmt.Errorf("worktree destination is registered to another source or session: %s", dest)
			}
			existing.LastAccessedAt = time.Now().UTC()
			m.records[existing.ID] = *existing
			_ = m.save()
			return *existing, true, nil
		}
	}
	if _, err := os.Lstat(dest); err == nil {
		return Record{}, false, fmt.Errorf("worktree destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return Record{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Record{}, false, err
	}
	created := false
	cleanup := func() {
		if !created {
			return
		}
		if req.WorktreeType == "standalone" {
			_ = os.RemoveAll(dest)
		} else {
			_, _ = gitOutput(context.Background(), root, "worktree", "remove", "--force", dest)
		}
	}
	if req.WorktreeType == "standalone" {
		if err := run(ctx, "", nil, "git", "clone", "--no-hardlinks", "--no-checkout", root, dest); err != nil {
			return Record{}, false, err
		}
		created = true
		if _, err := gitOutput(ctx, dest, "checkout", "--detach", commit); err != nil {
			cleanup()
			return Record{}, false, err
		}
	} else {
		if _, err := gitOutput(ctx, root, "worktree", "add", "--detach", dest, commit); err != nil {
			return Record{}, false, err
		}
		created = true
	}
	if req.CopyMode == "dirty" && req.GitRef == "" {
		if err := copyDirty(ctx, root, dest); err != nil {
			cleanup()
			return Record{}, false, err
		}
	}
	now := time.Now().UTC()
	id := "wt-" + randomHex(8)
	record := Record{
		ID: id, Path: dest, SourceRepo: root, RepoName: filepath.Base(root), Kind: "session",
		CreationMode: req.WorktreeType, GitRef: req.GitRef, HeadCommit: commit,
		SessionID: req.SessionID, CreatedAt: now, LastAccessedAt: now, Status: "alive", Label: label,
	}
	m.records[id] = record
	if err := m.save(); err != nil {
		delete(m.records, id)
		cleanup()
		return Record{}, false, err
	}
	return record, false, nil
}

func (m *Manager) List(repo string, types []string, includeAll bool) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	allowed := make(map[string]bool, len(types))
	for _, kind := range types {
		allowed[kind] = true
	}
	result := make([]Record, 0, len(m.records))
	for id, record := range m.records {
		if _, err := os.Stat(record.Path); err != nil {
			record.Status = "dead"
			m.records[id] = record
		}
		if !includeAll && record.Status != "alive" {
			continue
		}
		if repo != "" && record.SourceRepo != repo && record.RepoName != repo {
			continue
		}
		if len(allowed) > 0 && !allowed[record.Kind] {
			continue
		}
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (m *Manager) Show(idOrPath string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if record, ok := m.records[idOrPath]; ok {
		return record, true
	}
	if abs, err := filepath.Abs(idOrPath); err == nil {
		if record := m.recordByPath(abs); record != nil {
			return *record, true
		}
	}
	return Record{}, false
}

func (m *Manager) Remove(ctx context.Context, req RemoveRequest) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (req.WorktreePath == "") == (req.IDOrPath == "") {
		return false, "", errors.New("exactly one of worktreePath or idOrPath must be set")
	}
	key := req.IDOrPath
	if key == "" {
		key = req.WorktreePath
	}
	record, ok := m.records[key]
	if !ok {
		abs, err := filepath.Abs(key)
		if err != nil {
			return false, "", err
		}
		found := m.recordByPath(abs)
		if found == nil {
			return false, "", fmt.Errorf("worktree not found: %s", key)
		}
		record = *found
	}
	if req.DryRun {
		return false, record.Path, nil
	}
	if record.CreationMode == "standalone" {
		if err := validateStandalone(record); err != nil {
			return false, record.Path, err
		}
		if err := os.RemoveAll(record.Path); err != nil {
			return false, record.Path, err
		}
	} else {
		args := []string{"worktree", "remove"}
		if req.Force {
			args = append(args, "--force")
		}
		args = append(args, record.Path)
		if _, err := gitOutput(ctx, record.SourceRepo, args...); err != nil {
			return false, record.Path, err
		}
	}
	delete(m.records, record.ID)
	if err := m.save(); err != nil {
		return false, record.Path, err
	}
	return true, record.Path, nil
}

func validateStandalone(record Record) error {
	if info, err := os.Stat(filepath.Join(record.Path, ".git")); err != nil || !info.IsDir() {
		return fmt.Errorf("refusing to remove %q: standalone .git directory is missing", record.Path)
	}
	root, err := gitOutput(context.Background(), record.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(strings.TrimSpace(root))
	resolvedRecordPath, recordErr := filepath.EvalSymlinks(record.Path)
	if err != nil || recordErr != nil || filepath.Clean(resolvedRoot) != filepath.Clean(resolvedRecordPath) {
		return fmt.Errorf("refusing to remove %q: repository identity changed", record.Path)
	}
	origin, err := gitOutput(context.Background(), record.Path, "config", "--get", "remote.origin.url")
	if err != nil {
		return err
	}
	origin = strings.TrimSpace(origin)
	if !filepath.IsAbs(origin) {
		return fmt.Errorf("refusing to remove %q: standalone origin changed", record.Path)
	}
	resolvedOrigin, err := filepath.EvalSymlinks(origin)
	if err != nil || filepath.Clean(resolvedOrigin) != filepath.Clean(record.SourceRepo) {
		return fmt.Errorf("refusing to remove %q: standalone origin changed", record.Path)
	}
	return nil
}

func (m *Manager) StatePath() string { return m.path }

func (m *Manager) recordByPath(path string) *Record {
	clean := filepath.Clean(path)
	for _, record := range m.records {
		if filepath.Clean(record.Path) == clean {
			copy := record
			return &copy
		}
	}
	return nil
}

func (m *Manager) save() error {
	records := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(m.path), ".gork-go-worktrees-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.path)
}

func copyDirty(ctx context.Context, source, dest string) error {
	staged, err := gitBytes(ctx, source, "diff", "--cached", "--binary", "--no-ext-diff")
	if err != nil {
		return err
	}
	if len(staged) > 0 {
		if err := run(ctx, dest, staged, "git", "apply", "--index", "--binary", "-"); err != nil {
			return fmt.Errorf("copy staged changes: %w", err)
		}
	}
	working, err := gitBytes(ctx, source, "diff", "--binary", "--no-ext-diff")
	if err != nil {
		return err
	}
	if len(working) > 0 {
		if err := run(ctx, dest, working, "git", "apply", "--binary", "-"); err != nil {
			return fmt.Errorf("copy working changes: %w", err)
		}
	}
	untracked, err := gitBytes(ctx, source, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, raw := range bytes.Split(untracked, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		rel := filepath.Clean(string(raw))
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("unsafe untracked path %q", rel)
		}
		if err := copyEntry(filepath.Join(source, rel), filepath.Join(dest, rel)); err != nil {
			return err
		}
	}
	return nil
}

func copyEntry(source, dest string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, dest)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported untracked file type: %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := gitBytes(ctx, dir, args...)
	return string(data), err
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func run(ctx context.Context, dir string, stdin []byte, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = bytes.NewReader(stdin)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func sanitizeLabel(value string) string {
	var result strings.Builder
	previousDash := false
	for _, char := range strings.ToLower(value) {
		if char == ' ' || char == '_' {
			char = '-'
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			result.WriteRune(char)
			previousDash = false
		} else if char == '-' && !previousDash && result.Len() > 0 {
			result.WriteByte('-')
			previousDash = true
		}
		if result.Len() >= 64 {
			break
		}
	}
	return strings.Trim(result.String(), "-")
}

func randomHex(bytesCount int) string {
	data := make([]byte, bytesCount)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}
