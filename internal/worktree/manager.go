package worktree

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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

type ForkRequest struct {
	SourceWorktreePath string `json:"sourceWorktreePath"`
	NewSessionID       string `json:"newSessionId"`
	CopyMode           string `json:"copyMode"`
	GitRef             string `json:"gitRef"`
	WorktreeType       string `json:"worktreeType"`
	Label              string `json:"label"`
}

type ForkResponse struct {
	Status        string  `json:"status"`
	NewSessionID  string  `json:"newSessionId"`
	WorktreePath  string  `json:"worktreePath"`
	Commit        *string `json:"commit,omitempty"`
	SourceGitRoot string  `json:"sourceGitRoot,omitempty"`
}

type RemoveRequest struct {
	WorktreePath string `json:"worktreePath"`
	IDOrPath     string `json:"idOrPath"`
	Force        bool   `json:"force"`
	DryRun       bool   `json:"dryRun"`
}

type ApplyRequest struct {
	SessionID    string `json:"sessionId"`
	WorktreePath string `json:"worktreePath"`
	Mode         string `json:"mode"`
}

type FileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Type      string `json:"type"`
	Additions uint64 `json:"additions"`
	Deletions uint64 `json:"deletions"`
}

type FileConflict struct {
	Path   string  `json:"path"`
	Type   string  `json:"type"`
	Base   *string `json:"base"`
	Ours   *string `json:"ours"`
	Theirs *string `json:"theirs"`
}

type ApplyResponse struct {
	Status    string         `json:"status"`
	Files     []FileChange   `json:"files"`
	GitRoot   string         `json:"gitRoot,omitempty"`
	Conflicts []FileConflict `json:"conflicts,omitempty"`
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
	CreatorPID     int       `json:"creatorPid,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	LastAccessedAt time.Time `json:"lastAccessedAt"`
	Status         string    `json:"status"`
	Label          string    `json:"label"`
}

type GCReport struct {
	DeadRemoved    uint64 `json:"deadRemoved"`
	ExpiredRemoved uint64 `json:"expiredRemoved"`
	SkippedAlive   uint64 `json:"skippedAlive"`
	RemoveFailed   uint64 `json:"removeFailed"`
}

type DBStats struct {
	TotalRecords uint64 `json:"totalRecords"`
	AliveCount   uint64 `json:"aliveCount"`
	DeadCount    uint64 `json:"deadCount"`
	DBFileBytes  uint64 `json:"dbFileBytes"`
}

type RebuildReport struct {
	Discovered     uint64 `json:"discovered"`
	Registered     uint64 `json:"registered"`
	AlreadyTracked uint64 `json:"alreadyTracked"`
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
	mainRoot, err := mainRepositoryRoot(ctx, root)
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
		dest = filepath.Join(m.base, sanitizeLabel(filepath.Base(mainRoot)), label)
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return Record{}, false, err
	}
	if existing := m.recordByPath(dest); existing != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			if existing.SourceRepo != mainRoot || existing.SessionID != req.SessionID {
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
		ID: id, Path: dest, SourceRepo: mainRoot, RepoName: filepath.Base(mainRoot), Kind: "session",
		CreationMode: req.WorktreeType, GitRef: req.GitRef, HeadCommit: commit,
		SessionID: req.SessionID, CreatorPID: os.Getpid(), CreatedAt: now, LastAccessedAt: now, Status: "alive", Label: label,
	}
	m.records[id] = record
	if err := m.save(); err != nil {
		delete(m.records, id)
		cleanup()
		return Record{}, false, err
	}
	return record, false, nil
}

func (m *Manager) CreateFromWorktree(ctx context.Context, req ForkRequest) (ForkResponse, bool, error) {
	if req.SourceWorktreePath == "" || req.NewSessionID == "" {
		return ForkResponse{}, false, errors.New("sourceWorktreePath and newSessionId are required")
	}
	record, existed, err := m.Create(ctx, CreateRequest{
		SessionID: req.NewSessionID, SourcePath: req.SourceWorktreePath,
		CopyMode: req.CopyMode, GitRef: req.GitRef, WorktreeType: req.WorktreeType, Label: req.Label,
	})
	if err != nil {
		return ForkResponse{}, false, err
	}
	commit := record.HeadCommit
	status := "created"
	if existed {
		status = "exists"
	}
	return ForkResponse{Status: status, NewSessionID: req.NewSessionID, WorktreePath: record.Path, Commit: &commit, SourceGitRoot: record.SourceRepo}, existed, nil
}

func mainRepositoryRoot(ctx context.Context, worktreeRoot string) (string, error) {
	common, err := gitOutput(ctx, worktreeRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(worktreeRoot, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", err
	}
	if filepath.Base(common) != ".git" {
		return worktreeRoot, nil
	}
	return filepath.Dir(common), nil
}

func EffectiveCWD(ctx context.Context, sourceCWD, newRoot string) (string, error) {
	root, err := gitOutput(ctx, sourceCWD, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(sourceCWD)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("sourceCwd is outside its Git worktree")
	}
	return filepath.Join(newRoot, rel), nil
}

func MainRoot(ctx context.Context, cwd string) (string, error) {
	root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	return mainRepositoryRoot(ctx, root)
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

func (m *Manager) Apply(ctx context.Context, req ApplyRequest) (ApplyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.SessionID == "" || req.WorktreePath == "" {
		return ApplyResponse{}, errors.New("sessionId and worktreePath are required")
	}
	if req.Mode == "" {
		req.Mode = "overwrite"
	}
	if req.Mode != "overwrite" && req.Mode != "merge" {
		return ApplyResponse{}, errors.New("mode must be overwrite or merge")
	}
	abs, err := filepath.Abs(req.WorktreePath)
	if err != nil {
		return ApplyResponse{}, err
	}
	record := m.recordByPath(abs)
	if record == nil || record.SessionID != req.SessionID {
		return ApplyResponse{}, errors.New("worktree is not registered to this session")
	}
	base, err := gitOutput(ctx, record.SourceRepo, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return ApplyResponse{}, err
	}
	base = strings.TrimSpace(base)
	if _, err := gitOutput(ctx, record.Path, "cat-file", "-e", base+"^{commit}"); err != nil {
		if _, fetchErr := gitOutput(ctx, record.Path, "fetch", "--no-tags", record.SourceRepo, base); fetchErr != nil {
			return ApplyResponse{}, fmt.Errorf("load main repository HEAD into worktree: %w", fetchErr)
		}
	}
	changes, err := changedFiles(ctx, record.Path, base)
	if err != nil {
		return ApplyResponse{}, err
	}
	response := ApplyResponse{Status: "success", Files: make([]FileChange, 0), GitRoot: record.SourceRepo}
	for _, change := range changes {
		if err := safeRelative(change.Path); err != nil {
			return ApplyResponse{}, err
		}
		baseData := gitFile(ctx, record.Path, base, change.Path)
		ours, oursMode, err := readOptional(filepath.Join(record.SourceRepo, filepath.FromSlash(change.Path)))
		if err != nil {
			return ApplyResponse{}, err
		}
		theirs, theirsMode, err := readOptional(filepath.Join(record.Path, filepath.FromSlash(change.Path)))
		if err != nil {
			return ApplyResponse{}, err
		}
		if req.Mode == "overwrite" || optionalBytesEqual(baseData, ours) {
			mode := oursMode
			if theirsMode != 0 {
				mode = theirsMode
			}
			if mode == 0 {
				mode = 0o644
			}
			if err := applyContent(filepath.Join(record.SourceRepo, filepath.FromSlash(change.Path)), theirs, mode); err != nil {
				return ApplyResponse{}, err
			}
			response.Files = append(response.Files, change)
			continue
		}
		if !optionalBytesEqual(baseData, theirs) {
			baseText, err := optionalText(baseData)
			if err != nil {
				return ApplyResponse{}, fmt.Errorf("binary merge conflict in %s", change.Path)
			}
			oursText, err := optionalText(ours)
			if err != nil {
				return ApplyResponse{}, fmt.Errorf("binary merge conflict in %s", change.Path)
			}
			theirsText, err := optionalText(theirs)
			if err != nil {
				return ApplyResponse{}, fmt.Errorf("binary merge conflict in %s", change.Path)
			}
			response.Conflicts = append(response.Conflicts, FileConflict{
				Path: change.Path, Type: change.Type, Base: baseText, Ours: oursText, Theirs: theirsText,
			})
		}
	}
	if len(response.Conflicts) > 0 {
		response.Status = "conflicts"
		response.GitRoot = ""
	}
	return response, nil
}

func changedFiles(ctx context.Context, worktreePath, base string) ([]FileChange, error) {
	data, err := gitBytes(ctx, worktreePath, "diff", "--name-status", "-z", base, "--")
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(data, []byte{0})
	changes := make(map[string]FileChange)
	for index := 0; index < len(parts) && len(parts[index]) > 0; {
		status := string(parts[index])
		index++
		if index >= len(parts) {
			return nil, errors.New("invalid git name-status output")
		}
		oldPath := ""
		path := filepath.ToSlash(string(parts[index]))
		index++
		kind := changeType(status)
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && index < len(parts) {
			oldPath, path = path, filepath.ToSlash(string(parts[index]))
			index++
		}
		changes[path] = FileChange{Path: path, OldPath: oldPath, Type: kind}
	}
	untracked, err := gitBytes(ctx, worktreePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, raw := range bytes.Split(untracked, []byte{0}) {
		if len(raw) > 0 {
			path := filepath.ToSlash(string(raw))
			changes[path] = FileChange{Path: path, Type: "untracked"}
		}
	}
	result := make([]FileChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func changeType(status string) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return "create"
	case strings.HasPrefix(status, "D"):
		return "delete"
	case strings.HasPrefix(status, "R"):
		return "rename"
	case strings.HasPrefix(status, "C"):
		return "copy"
	case strings.HasPrefix(status, "T"):
		return "typechange"
	default:
		return "edit"
	}
}

func gitFile(ctx context.Context, dir, commit, path string) []byte {
	data, err := gitBytes(ctx, dir, "show", commit+":"+path)
	if err != nil {
		return nil
	}
	return data
}

func readOptional(path string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%q is not a regular file", path)
	}
	return data, info.Mode().Perm(), nil
}

func optionalBytesEqual(left, right []byte) bool {
	return (left == nil) == (right == nil) && bytes.Equal(left, right)
}

func optionalText(data []byte) (*string, error) {
	if data == nil {
		return nil, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("binary content")
	}
	text := string(data)
	return &text, nil
}

func applyContent(path string, data []byte, mode os.FileMode) error {
	if data == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gork-go-apply-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
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
	return os.Rename(name, path)
}

func safeRelative(path string) error {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe worktree change path %q", path)
	}
	return nil
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

func (m *Manager) Stats() DBStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := DBStats{TotalRecords: uint64(len(m.records))}
	for _, record := range m.records {
		if record.Status == "dead" {
			stats.DeadCount++
		} else {
			stats.AliveCount++
		}
	}
	if info, err := os.Stat(m.path); err == nil {
		stats.DBFileBytes = uint64(info.Size())
	}
	return stats
}

func (m *Manager) GC(ctx context.Context, dryRun bool, maxAge *time.Duration, force bool) (GCReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var report GCReport
	now := time.Now().UTC()
	for id, record := range m.records {
		_, statErr := os.Stat(record.Path)
		missing := os.IsNotExist(statErr)
		if record.Status == "dead" || missing {
			report.DeadRemoved++
			if !dryRun {
				delete(m.records, id)
			}
			continue
		}
		if statErr != nil || maxAge == nil || !record.LastAccessedAt.Before(now.Add(-max(0, *maxAge))) {
			continue
		}
		if !force && processAlive(record.CreatorPID) {
			report.SkippedAlive++
			continue
		}
		if dryRun {
			report.ExpiredRemoved++
			continue
		}
		if err := removeRecord(ctx, record, force); err != nil {
			report.RemoveFailed++
			continue
		}
		delete(m.records, id)
		report.ExpiredRemoved++
	}
	if !dryRun {
		return report, m.save()
	}
	return report, nil
}

func (m *Manager) Rebuild(ctx context.Context) (RebuildReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var report RebuildReport
	entries, err := filepath.Glob(filepath.Join(m.base, "*", "*"))
	if err != nil {
		return report, err
	}
	for _, path := range entries {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		gitInfo, err := os.Stat(filepath.Join(path, ".git"))
		if err != nil {
			continue
		}
		report.Discovered++
		if m.recordByPath(path) != nil {
			report.AlreadyTracked++
			continue
		}
		root, err := mainRepositoryRoot(ctx, path)
		if err != nil {
			continue
		}
		mode := "linked"
		if gitInfo.IsDir() {
			mode = "standalone"
			if origin, originErr := gitOutput(ctx, path, "config", "--get", "remote.origin.url"); originErr == nil && filepath.IsAbs(strings.TrimSpace(origin)) {
				if resolved, resolveErr := filepath.EvalSymlinks(strings.TrimSpace(origin)); resolveErr == nil {
					root = resolved
				}
			}
		}
		head, err := gitOutput(ctx, path, "rev-parse", "HEAD^{commit}")
		if err != nil {
			continue
		}
		now := time.Now().UTC()
		id := "wt-" + randomHex(8)
		m.records[id] = Record{ID: id, Path: path, SourceRepo: root, RepoName: filepath.Base(root), Kind: "session", CreationMode: mode, HeadCommit: strings.TrimSpace(head), CreatedAt: info.ModTime().UTC(), LastAccessedAt: now, Status: "alive", Label: filepath.Base(path)}
		report.Registered++
	}
	if report.Registered > 0 {
		return report, m.save()
	}
	return report, nil
}

func removeRecord(ctx context.Context, record Record, force bool) error {
	if record.CreationMode == "standalone" {
		if err := validateStandalone(record); err != nil {
			return err
		}
		return os.RemoveAll(record.Path)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, record.Path)
	_, err := gitOutput(ctx, record.SourceRepo, args...)
	return err
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

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
	return cloneFile(source, dest, info.Mode().Perm())
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
