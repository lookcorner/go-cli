package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var snapshotGitConfig = []string{
	"-c", "core.autocrlf=false",
	"-c", "core.longpaths=true",
	"-c", "core.symlinks=true",
	"-c", "core.quotepath=false",
	"-c", "core.fsmonitor=false",
}

type RehydrateRequest struct {
	SessionID    string
	SourceRepo   string
	WorktreePath string
	SnapshotRef  string
	Label        string
}

// SnapshotToRef stores HEAD plus all tracked and non-ignored working-tree
// changes in a synthetic commit without changing the real Git index.
func SnapshotToRef(ctx context.Context, worktreePath, refName, message string) (string, error) {
	if !strings.HasPrefix(refName, "refs/") {
		return "", errors.New("snapshot ref must be fully qualified")
	}
	index, err := unusedTempPath()
	if err != nil {
		return "", err
	}
	defer os.Remove(index)
	defer os.Remove(index + ".lock")
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := snapshotGit(ctx, worktreePath, env, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("seed snapshot index: %w", err)
	}
	if _, err := snapshotGit(ctx, worktreePath, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage snapshot: %w", err)
	}
	tree, err := snapshotGit(ctx, worktreePath, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write snapshot tree: %w", err)
	}
	identity := []string{
		"GIT_AUTHOR_NAME=Gork Snapshot",
		"GIT_AUTHOR_EMAIL=gork-snapshot@example.invalid",
		"GIT_COMMITTER_NAME=Gork Snapshot",
		"GIT_COMMITTER_EMAIL=gork-snapshot@example.invalid",
	}
	commit, err := snapshotGit(ctx, worktreePath, identity, "commit-tree", tree, "-p", "HEAD", "-m", message)
	if err != nil {
		return "", fmt.Errorf("commit snapshot tree: %w", err)
	}
	if _, err := snapshotGit(ctx, worktreePath, nil, "update-ref", refName, commit); err != nil {
		return "", fmt.Errorf("update snapshot ref: %w", err)
	}
	return commit, nil
}

// TransferSnapshot copies a snapshot and its objects from an independent
// worktree repository into the durable source repository.
func TransferSnapshot(ctx context.Context, worktreePath, sourceRepo, refName string) error {
	if !strings.HasPrefix(refName, "refs/") {
		return errors.New("snapshot ref must be fully qualified")
	}
	worktreeCommon, worktreeErr := snapshotGit(ctx, worktreePath, nil, "rev-parse", "--git-common-dir")
	sourceCommon, sourceErr := snapshotGit(ctx, sourceRepo, nil, "rev-parse", "--git-common-dir")
	if worktreeErr == nil && sourceErr == nil {
		worktreeCommon = absoluteGitDir(worktreePath, worktreeCommon)
		sourceCommon = absoluteGitDir(sourceRepo, sourceCommon)
		if worktreeCommon == sourceCommon {
			_, err := snapshotGit(ctx, sourceRepo, nil, "rev-parse", "--verify", refName+"^{commit}")
			return err
		}
	}
	refspec := "+" + refName + ":" + refName
	if _, err := snapshotGit(ctx, sourceRepo, nil, "fetch", "--no-tags", worktreePath, refspec); err != nil {
		return fmt.Errorf("transfer snapshot: %w", err)
	}
	if _, err := snapshotGit(ctx, sourceRepo, nil, "rev-parse", "--verify", refName+"^{commit}"); err != nil {
		return fmt.Errorf("verify transferred snapshot: %w", err)
	}
	return nil
}

func DeleteSnapshotRef(ctx context.Context, repo, refName string) error {
	if !strings.HasPrefix(refName, "refs/") {
		return errors.New("snapshot ref must be fully qualified")
	}
	_, err := snapshotGit(ctx, repo, nil, "update-ref", "-d", refName)
	return err
}

// Rehydrate recreates a linked worktree whose HEAD remains at the snapshot's
// original base while the captured tree is restored as uncommitted changes.
func (m *Manager) Rehydrate(ctx context.Context, req RehydrateRequest) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.SessionID == "" || req.SourceRepo == "" || req.WorktreePath == "" || req.SnapshotRef == "" {
		return Record{}, errors.New("sessionId, sourceRepo, worktreePath, and snapshotRef are required")
	}
	source, err := filepath.Abs(req.SourceRepo)
	if err != nil {
		return Record{}, err
	}
	dest, err := filepath.Abs(req.WorktreePath)
	if err != nil {
		return Record{}, err
	}
	if filepath.Clean(source) == filepath.Clean(dest) {
		return Record{}, errors.New("worktree destination must differ from source repository")
	}
	if _, err := os.Lstat(dest); err == nil {
		return Record{}, fmt.Errorf("worktree destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return Record{}, err
	}
	if _, err := snapshotGit(ctx, source, nil, "worktree", "prune"); err != nil {
		return Record{}, err
	}
	snapshot, err := snapshotGit(ctx, source, nil, "rev-parse", "--verify", req.SnapshotRef+"^{commit}")
	if err != nil {
		return Record{}, fmt.Errorf("resolve snapshot: %w", err)
	}
	base, baseErr := snapshotGit(ctx, source, nil, "rev-parse", "--verify", "--quiet", snapshot+"^")
	if baseErr != nil {
		base = snapshot
	} else if _, err := snapshotGit(ctx, source, nil, "cat-file", "-e", base+"^{commit}"); err != nil {
		base = snapshot
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Record{}, err
	}
	if _, err := snapshotGit(ctx, source, nil, "worktree", "add", "--detach", "--no-checkout", dest, base); err != nil {
		return Record{}, fmt.Errorf("create rehydrated worktree: %w", err)
	}
	cleanup := func() {
		_, _ = snapshotGit(context.Background(), source, nil, "worktree", "remove", "--force", dest)
		_, _ = snapshotGit(context.Background(), source, nil, "worktree", "prune")
	}
	if _, err := snapshotGit(ctx, dest, nil, "read-tree", "--reset", "-u", snapshot); err != nil {
		cleanup()
		return Record{}, fmt.Errorf("restore snapshot tree: %w", err)
	}
	head, err := snapshotGit(ctx, dest, nil, "rev-parse", "HEAD^{commit}")
	if err != nil {
		cleanup()
		return Record{}, err
	}
	mainRoot, err := mainRepositoryRoot(ctx, source)
	if err != nil {
		cleanup()
		return Record{}, err
	}
	now := time.Now().UTC()
	label := sanitizeLabel(req.Label)
	if label == "" {
		label = filepath.Base(dest)
	}
	id := "wt-" + randomHex(8)
	var replaced *Record
	if existing := m.recordByPath(dest); existing != nil {
		copy := *existing
		replaced = &copy
		id = existing.ID
	}
	record := Record{
		ID: id, Path: dest, SourceRepo: mainRoot, RepoName: filepath.Base(mainRoot),
		Kind: "subagent", CreationMode: "linked", GitRef: req.SnapshotRef, HeadCommit: head,
		SessionID: req.SessionID, CreatorPID: os.Getpid(), CreatedAt: now, LastAccessedAt: now,
		Status: "alive", Label: label,
	}
	m.records[record.ID] = record
	if err := m.save(); err != nil {
		if replaced == nil {
			delete(m.records, record.ID)
		} else {
			m.records[record.ID] = *replaced
		}
		cleanup()
		return Record{}, err
	}
	return record, nil
}

func snapshotGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	fullArgs := append(append([]string{}, snapshotGitConfig...), args...)
	command := exec.CommandContext(ctx, "git", fullArgs...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_LFS_SKIP_SMUDGE=1")
	command.Env = append(command.Env, extraEnv...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func unusedTempPath() (string, error) {
	file, err := os.CreateTemp("", "gork-snapshot-index-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func absoluteGitDir(repo, gitDir string) string {
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	resolved, err := filepath.EvalSymlinks(gitDir)
	if err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(gitDir)
}
