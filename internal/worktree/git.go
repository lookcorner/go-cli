package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
)

type GitFileChange struct {
	Path       string  `json:"path"`
	OldPath    string  `json:"oldPath,omitempty"`
	Type       string  `json:"type"`
	Staged     bool    `json:"staged"`
	Additions  uint64  `json:"additions"`
	Deletions  uint64  `json:"deletions"`
	Patch      *string `json:"patch,omitempty"`
	PatchBytes *uint64 `json:"patchBytes,omitempty"`
	PatchLines *uint64 `json:"patchLines,omitempty"`
}

type GitStatus struct {
	Root       string          `json:"root,omitempty"`
	MainRoot   string          `json:"mainRoot,omitempty"`
	IsWorktree bool            `json:"isWorktree"`
	Branch     string          `json:"branch,omitempty"`
	Commit     string          `json:"commit,omitempty"`
	Upstream   string          `json:"upstream,omitempty"`
	RemoteURL  string          `json:"remoteUrl,omitempty"`
	Ahead      *uint64         `json:"ahead,omitempty"`
	Behind     *uint64         `json:"behind,omitempty"`
	Staged     []GitFileChange `json:"staged"`
	Unstaged   []GitFileChange `json:"unstaged"`
}

type GitHead struct {
	Branch     string
	Root       string
	MainRoot   string
	IsWorktree bool
}

type HeadDivergence struct {
	SessionCommit string `json:"sessionCommit"`
	CurrentCommit string `json:"currentCommit"`
	SessionBranch string `json:"sessionBranch,omitempty"`
}

func DetectHeadDivergence(sessionCommit, sessionBranch, currentCommit string) *HeadDivergence {
	if sessionCommit == "" || currentCommit == "" || sessionCommit == currentCommit {
		return nil
	}
	return &HeadDivergence{
		SessionCommit: sessionCommit,
		CurrentCommit: currentCommit,
		SessionBranch: sessionBranch,
	}
}

type GitInfo struct {
	Root          string   `json:"root"`
	Remotes       []string `json:"remotes"`
	CurrentBranch string   `json:"currentBranch,omitempty"`
	DefaultBranch string   `json:"defaultBranch,omitempty"`
	VCSKind       string   `json:"vcsKind,omitempty"`
}

type GitBranch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Remote  bool   `json:"remote"`
}

type GitBranches struct {
	CurrentBranch string      `json:"currentBranch,omitempty"`
	RepoRoot      string      `json:"repoRoot"`
	Branches      []GitBranch `json:"branches"`
}

type CheckoutCommitResult struct {
	CheckedOut bool   `json:"checked_out"`
	Stashed    bool   `json:"stashed"`
	Fetched    bool   `json:"fetched"`
	Error      string `json:"error,omitempty"`
}

type CommitData struct {
	CommitHash string `json:"commitHash,omitempty"`
	Output     string `json:"output,omitempty"`
}

type GitReadFile struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Content  string `json:"content"`
	IsBinary bool   `json:"isBinary"`
}

type GitReadError struct {
	Path    string `json:"path,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GitReadFiles struct {
	Files  []GitReadFile  `json:"files"`
	Errors []GitReadError `json:"errors"`
}

type GitDiffFile struct {
	Path       string  `json:"path"`
	OldPath    string  `json:"oldPath,omitempty"`
	Type       string  `json:"type"`
	Additions  uint64  `json:"additions"`
	Deletions  uint64  `json:"deletions"`
	Patch      *string `json:"patch,omitempty"`
	PatchBytes *uint64 `json:"patchBytes,omitempty"`
	PatchLines *uint64 `json:"patchLines,omitempty"`
	OldText    *string `json:"oldText,omitempty"`
	NewText    *string `json:"newText,omitempty"`
}

type GitDiffs struct {
	Files []GitDiffFile `json:"files"`
}

func GitRoot(ctx context.Context, cwd string) (string, error) {
	root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(root), err
}

func CurrentCommit(ctx context.Context, root string) (string, error) {
	commit, err := gitOutput(ctx, root, "rev-parse", "HEAD^{commit}")
	return strings.TrimSpace(commit), err
}

func HeadInfo(ctx context.Context, cwd string) (GitHead, error) {
	root, err := GitRoot(ctx, cwd)
	if err != nil {
		return GitHead{}, err
	}
	mainRoot, err := mainRepositoryRoot(ctx, root)
	if err != nil {
		return GitHead{}, err
	}
	return GitHead{
		Branch: optionalGit(ctx, root, "symbolic-ref", "--short", "-q", "HEAD"),
		Root:   root, MainRoot: mainRoot, IsWorktree: root != mainRoot,
	}, nil
}

// WatchHead reports repository events after a short debounce. Callers compare
// Head snapshots, so commits that keep the same branch are naturally ignored.
func WatchHead(ctx context.Context, cwd string, ready, changed func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	paths := make(map[string]bool)
	add := func(path string) error {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" || paths[path] {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			return err
		}
		paths[path] = true
		return nil
	}
	if err := add(cwd); err != nil {
		return fmt.Errorf("watch Git HEAD in %s: %w", cwd, err)
	}
	addRepository := func() bool {
		root, rootErr := GitRoot(ctx, cwd)
		if rootErr != nil {
			return false
		}
		if add(root) != nil {
			return false
		}
		gitDir, gitErr := gitOutput(ctx, root, "rev-parse", "--absolute-git-dir")
		return gitErr == nil && add(gitDir) == nil
	}
	repositoryWatched := addRepository()
	if ready != nil {
		ready()
	}

	var timer *time.Timer
	var timerC <-chan time.Time
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
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(50 * time.Millisecond)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(50 * time.Millisecond)
			}
			timerC = timer.C
		case <-timerC:
			timerC = nil
			if !repositoryWatched {
				repositoryWatched = addRepository()
			}
			if changed != nil {
				changed()
			}
		}
	}
}

func Info(ctx context.Context, root string) (GitInfo, error) {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return GitInfo{}, err
	}
	info := GitInfo{Root: resolved, CurrentBranch: optionalGit(ctx, resolved, "symbolic-ref", "--short", "-q", "HEAD"), VCSKind: "git"}
	names := strings.Fields(optionalGit(ctx, resolved, "remote"))
	seen := make(map[string]bool)
	for _, name := range names {
		urls := strings.Fields(optionalGit(ctx, resolved, "remote", "get-url", "--all", name))
		for _, url := range urls {
			if !seen[url] {
				seen[url] = true
				info.Remotes = append(info.Remotes, url)
			}
		}
	}
	sort.Strings(info.Remotes)
	defaultRef := optionalGit(ctx, resolved, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	info.DefaultBranch = strings.TrimPrefix(defaultRef, "origin/")
	if info.DefaultBranch == "" {
		info.DefaultBranch = optionalGit(ctx, resolved, "config", "--get", "init.defaultBranch")
	}
	return info, nil
}

func Branches(ctx context.Context, root string) (GitBranches, error) {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return GitBranches{}, err
	}
	result := GitBranches{CurrentBranch: optionalGit(ctx, resolved, "symbolic-ref", "--short", "-q", "HEAD"), RepoRoot: resolved}
	output, err := gitOutput(ctx, resolved, "for-each-ref", "--format=%(HEAD)%09%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return GitBranches{}, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || fields[1] == "" || strings.HasSuffix(fields[1], "/HEAD") {
			continue
		}
		remote := strings.HasPrefix(fields[1], "refs/remotes/")
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "refs/heads/"), "refs/remotes/")
		result.Branches = append(result.Branches, GitBranch{Name: name, Current: strings.TrimSpace(fields[0]) == "*", Remote: remote})
	}
	return result, nil
}

func Status(ctx context.Context, root string, includeUntracked, includeStats, ignoreSubmodules, includePatches bool) (GitStatus, error) {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return GitStatus{}, err
	}
	commit, err := CurrentCommit(ctx, resolved)
	if err != nil {
		return GitStatus{}, err
	}
	mainRoot, err := mainRepositoryRoot(ctx, resolved)
	if err != nil {
		return GitStatus{}, err
	}
	status := GitStatus{Root: resolved, MainRoot: mainRoot, IsWorktree: resolved != mainRoot, Commit: commit}
	status.Branch = optionalGit(ctx, resolved, "symbolic-ref", "--short", "-q", "HEAD")
	status.Upstream = optionalGit(ctx, resolved, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	status.RemoteURL = optionalGit(ctx, resolved, "config", "--get", "remote.origin.url")
	if status.Upstream != "" {
		if counts := strings.Fields(optionalGit(ctx, resolved, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")); len(counts) == 2 {
			ahead, aheadErr := strconv.ParseUint(counts[0], 10, 64)
			behind, behindErr := strconv.ParseUint(counts[1], 10, 64)
			if aheadErr == nil && behindErr == nil {
				status.Ahead, status.Behind = &ahead, &behind
			}
		}
	}
	status.Staged, err = gitChanges(ctx, resolved, true, false, includeStats, ignoreSubmodules, includePatches)
	if err != nil {
		return GitStatus{}, err
	}
	status.Unstaged, err = gitChanges(ctx, resolved, false, includeUntracked, includeStats, ignoreSubmodules, includePatches)
	return status, err
}

func Stage(ctx context.Context, root string, paths []string) ([]string, error) {
	args := []string{"add", "-A"}
	if len(paths) > 0 {
		args = append([]string{"add", "--"}, paths...)
	}
	_, err := gitOutput(ctx, root, args...)
	return paths, err
}

func Unstage(ctx context.Context, root string, paths []string) error {
	args := []string{"reset", "HEAD"}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	_, err := gitOutput(ctx, root, args...)
	return err
}

func Discard(ctx context.Context, root string, paths []string, scope string, includeUntracked bool) error {
	if scope == "" {
		scope = "both"
	}
	if scope != "working" && scope != "staged" && scope != "both" {
		return errors.New("scope must be working, staged, or both")
	}
	if scope == "staged" || scope == "both" {
		if err := Unstage(ctx, root, paths); err != nil {
			return err
		}
	}
	if scope == "working" || scope == "both" {
		args := []string{"checkout"}
		if len(paths) == 0 {
			args = append(args, ".")
		} else {
			args = append(args, "--")
			args = append(args, paths...)
		}
		if _, err := gitOutput(ctx, root, args...); err != nil && !includeUntracked {
			return err
		}
	}
	if includeUntracked {
		args := []string{"clean", "-fd"}
		if len(paths) > 0 {
			args = append(args, "--")
			args = append(args, paths...)
		}
		_, err := gitOutput(ctx, root, args...)
		return err
	}
	return nil
}

func Stash(ctx context.Context, root string, includeUntracked bool) error {
	args := []string{"stash", "push"}
	if includeUntracked {
		args = append(args, "--include-untracked")
	}
	_, err := gitOutput(ctx, root, args...)
	return err
}

func CheckoutBranch(ctx context.Context, root, branch string, create bool) error {
	if branch == "" {
		return errors.New("branch is required")
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("working tree has uncommitted changes; commit or stash before switching branches")
	}
	args := []string{"checkout"}
	if create {
		args = append(args, "-b")
	}
	args = append(args, branch)
	_, err = gitOutput(ctx, root, args...)
	return err
}

func CheckoutCommit(ctx context.Context, root, commit string, stashIfDirty bool) CheckoutCommitResult {
	if current, err := CurrentCommit(ctx, root); err == nil && current == commit {
		return CheckoutCommitResult{CheckedOut: true}
	}
	stashed := false
	if stashIfDirty && strings.TrimSpace(optionalGit(ctx, root, "status", "--porcelain")) != "" {
		message := "auto-stash before checkout " + commit
		if _, err := gitOutput(ctx, root, "stash", "push", "-m", message); err == nil {
			stashed = true
		}
	}
	if _, err := gitOutput(ctx, root, "checkout", commit); err == nil {
		return CheckoutCommitResult{CheckedOut: true, Stashed: stashed}
	}
	_, _ = gitOutput(ctx, root, "fetch", "origin")
	if _, err := gitOutput(ctx, root, "checkout", commit); err == nil {
		return CheckoutCommitResult{CheckedOut: true, Stashed: stashed, Fetched: true}
	} else {
		if stashed {
			_, _ = gitOutput(ctx, root, "stash", "pop")
		}
		return CheckoutCommitResult{Fetched: true, Error: err.Error()}
	}
}

func Commit(ctx context.Context, root, message string, amend, signoff, push, syncRemote bool) (CommitData, string, error) {
	if strings.TrimSpace(message) == "" {
		return CommitData{}, "", errors.New("message is required")
	}
	args := []string{"commit", "-m", message}
	if amend {
		args = append(args, "--amend")
	}
	if signoff {
		args = append(args, "--signoff")
	}
	if _, err := gitOutput(ctx, root, args...); err != nil {
		return CommitData{}, "", err
	}
	commit, _ := CurrentCommit(ctx, root)
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	data := CommitData{CommitHash: commit, Output: "Committed: " + short}
	warning := ""
	if syncRemote {
		if output, err := gitOutput(ctx, root, "pull", "--rebase"); err != nil {
			warning = "Couldn't pull the latest changes. " + err.Error()
		} else if strings.TrimSpace(output) != "" {
			data.Output += "\n--- Pull ---\n" + strings.TrimSpace(output)
		}
	}
	if warning == "" && (push || syncRemote) {
		if output, err := gitOutput(ctx, root, "push", "-u", "origin", "HEAD"); err != nil {
			warning = "Couldn't push your changes. " + err.Error()
		} else if strings.TrimSpace(output) != "" {
			data.Output += "\n--- Push ---\n" + strings.TrimSpace(output)
		}
	}
	return data, warning, nil
}

func ReadFiles(ctx context.Context, root string, paths []string, version string) (GitReadFiles, error) {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return GitReadFiles{}, err
	}
	if version == "" {
		version = "HEAD"
	}
	result := GitReadFiles{Files: make([]GitReadFile, 0, len(paths)), Errors: make([]GitReadError, 0)}
	for _, path := range paths {
		rel, err := relativeGitPath(resolved, path)
		if err != nil {
			return GitReadFiles{}, err
		}
		var data []byte
		switch version {
		case "working":
			data, err = os.ReadFile(filepath.Join(resolved, filepath.FromSlash(rel)))
		case "staged":
			data, err = gitBytes(ctx, resolved, "show", ":"+rel)
		default:
			data, err = gitBytes(ctx, resolved, "show", version+":"+rel)
		}
		if err != nil {
			result.Errors = append(result.Errors, GitReadError{Path: rel, Code: "READ_FAILED", Message: err.Error()})
			continue
		}
		binary := !utf8.Valid(data)
		content := string(data)
		if binary {
			content = ""
		}
		result.Files = append(result.Files, GitReadFile{Path: rel, Version: version, Content: content, IsBinary: binary})
	}
	return result, nil
}

func StageContent(ctx context.Context, root, path, content string) error {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return err
	}
	rel, err := relativeGitPath(resolved, path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp("", "gork-stage-content-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	hash, err := gitOutput(ctx, resolved, "hash-object", "-w", name)
	if err != nil {
		return err
	}
	mode := "100644"
	if entry := optionalGit(ctx, resolved, "ls-files", "-s", "--", rel); entry != "" {
		if fields := strings.Fields(entry); len(fields) > 0 {
			mode = fields[0]
		}
	}
	_, err = gitOutput(ctx, resolved, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(hash), rel)
	return err
}

func Diffs(ctx context.Context, root string, paths []string, from, to string, includePatch, includeContent, mergeBase bool) (GitDiffs, error) {
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return GitDiffs{}, err
	}
	if from == "" {
		from = "HEAD"
	}
	if to == "" {
		to = "working"
	}
	if mergeBase && from != "working" && from != "staged" && to != "working" && to != "staged" {
		if base := optionalGit(ctx, resolved, "merge-base", from, to); base != "" {
			from = base
		}
	}
	baseArgs, err := diffArgs(from, to)
	if err != nil {
		return GitDiffs{}, err
	}
	nameArgs := append([]string{"diff", "--name-status", "-z"}, baseArgs...)
	nameArgs = appendPaths(nameArgs, paths)
	data, err := gitBytes(ctx, resolved, nameArgs...)
	if err != nil {
		return GitDiffs{}, err
	}
	numstatArgs := append([]string{"diff", "--numstat"}, baseArgs...)
	numstatArgs = appendPaths(numstatArgs, paths)
	numstat, err := gitOutput(ctx, resolved, numstatArgs...)
	if err != nil {
		return GitDiffs{}, err
	}
	stats := parseNumstat(numstat)
	parts := bytes.Split(data, []byte{0})
	result := GitDiffs{Files: make([]GitDiffFile, 0, len(parts)/2)}
	for i := 0; i < len(parts) && len(parts[i]) > 0; {
		code := string(parts[i])
		i++
		if i >= len(parts) {
			return GitDiffs{}, errors.New("invalid git diff name-status output")
		}
		path := filepath.ToSlash(string(parts[i]))
		i++
		oldPath := ""
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i >= len(parts) {
				return GitDiffs{}, errors.New("invalid git diff rename output")
			}
			oldPath, path = path, filepath.ToSlash(string(parts[i]))
			i++
		}
		counts := stats[path]
		file := GitDiffFile{Path: path, OldPath: oldPath, Type: changeType(code), Additions: counts[0], Deletions: counts[1]}
		if includePatch {
			patchArgs := append([]string{"diff"}, baseArgs...)
			patchArgs = append(patchArgs, "--", path)
			patch, err := gitOutput(ctx, resolved, patchArgs...)
			if err != nil {
				return GitDiffs{}, err
			}
			patchBytes, patchLines := uint64(len(patch)), uint64(strings.Count(patch, "\n"))
			file.Patch, file.PatchBytes, file.PatchLines = &patch, &patchBytes, &patchLines
		}
		if includeContent {
			oldContentPath := path
			if oldPath != "" {
				oldContentPath = oldPath
			}
			file.OldText = readTextVersion(ctx, resolved, oldContentPath, from)
			file.NewText = readTextVersion(ctx, resolved, path, to)
		}
		result.Files = append(result.Files, file)
	}
	return result, nil
}

func diffArgs(from, to string) ([]string, error) {
	switch {
	case from == "staged" && to == "working":
		return nil, nil
	case to == "staged" && from != "working":
		return []string{"--cached", from}, nil
	case to == "working" && from != "staged":
		return []string{from}, nil
	case from != "working" && from != "staged" && to != "working" && to != "staged":
		return []string{from, to}, nil
	default:
		return nil, fmt.Errorf("unsupported git diff versions %q to %q", from, to)
	}
}

func appendPaths(args, paths []string) []string {
	args = append(args, "--")
	return append(args, paths...)
}

func parseNumstat(output string) map[string][2]uint64 {
	result := make(map[string][2]uint64)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		additions, _ := strconv.ParseUint(fields[0], 10, 64)
		deletions, _ := strconv.ParseUint(fields[1], 10, 64)
		result[filepath.ToSlash(fields[2])] = [2]uint64{additions, deletions}
	}
	return result
}

func readTextVersion(ctx context.Context, root, path, version string) *string {
	var data []byte
	var err error
	switch version {
	case "working":
		data, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	case "staged":
		data, err = gitBytes(ctx, root, "show", ":"+path)
	default:
		data, err = gitBytes(ctx, root, "show", version+":"+path)
	}
	if err != nil || !utf8.Valid(data) {
		return nil
	}
	text := string(data)
	return &text
}

func relativeGitPath(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is not within git repository %q", path, root)
		}
		path = rel
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid git path %q", path)
	}
	return filepath.ToSlash(clean), nil
}

func gitChanges(ctx context.Context, root string, staged, includeUntracked, includeStats, ignoreSubmodules, includePatches bool) ([]GitFileChange, error) {
	args := []string{"diff", "--name-status", "-z"}
	if staged {
		args = append(args, "--cached")
	}
	if ignoreSubmodules {
		args = append(args, "--ignore-submodules=all")
	}
	args = append(args, "--")
	data, err := gitBytes(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	stats := map[string][2]uint64{}
	if includeStats || includePatches {
		stats, err = gitNumstat(ctx, root, staged, ignoreSubmodules)
		if err != nil {
			return nil, err
		}
	}
	parts := bytes.Split(data, []byte{0})
	changes := make([]GitFileChange, 0, len(parts)/2)
	for i := 0; i < len(parts) && len(parts[i]) > 0; {
		code := string(parts[i])
		i++
		if i >= len(parts) {
			return nil, errors.New("invalid git name-status output")
		}
		path := filepath.ToSlash(string(parts[i]))
		i++
		oldPath := ""
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i >= len(parts) {
				return nil, errors.New("invalid git rename output")
			}
			oldPath, path = path, filepath.ToSlash(string(parts[i]))
			i++
		}
		counts := stats[path]
		change := GitFileChange{Path: path, OldPath: oldPath, Type: changeType(code), Staged: staged, Additions: counts[0], Deletions: counts[1]}
		if includePatches {
			patch, err := gitChangePatch(ctx, root, staged, path, ignoreSubmodules)
			if err != nil {
				return nil, err
			}
			patchBytes, patchLines := uint64(len(patch)), uint64(strings.Count(patch, "\n"))
			change.Patch, change.PatchBytes, change.PatchLines = &patch, &patchBytes, &patchLines
		}
		changes = append(changes, change)
	}
	if includeUntracked {
		untracked, err := gitBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		for _, raw := range bytes.Split(untracked, []byte{0}) {
			path := filepath.ToSlash(string(raw))
			if len(raw) > 0 && (!ignoreSubmodules || !nestedGitRepository(root, path)) {
				changes = append(changes, GitFileChange{Path: path, Type: "untracked"})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func gitNumstat(ctx context.Context, root string, staged, ignoreSubmodules bool) (map[string][2]uint64, error) {
	args := []string{"diff", "--numstat"}
	if staged {
		args = append(args, "--cached")
	}
	if ignoreSubmodules {
		args = append(args, "--ignore-submodules=all")
	}
	args = append(args, "--")
	output, err := gitOutput(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	return parseNumstat(output), nil
}

func gitChangePatch(ctx context.Context, root string, staged bool, path string, ignoreSubmodules bool) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	if ignoreSubmodules {
		args = append(args, "--ignore-submodules=all")
	}
	args = append(args, "--", path)
	return gitOutput(ctx, root, args...)
}

func nestedGitRepository(root, path string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path), ".git"))
	return err == nil
}

func optionalGit(ctx context.Context, root string, args ...string) string {
	value, err := gitOutput(ctx, root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func ValidateGitRoot(ctx context.Context, root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("gitRoot or sessionId is required")
	}
	resolved, err := GitRoot(ctx, root)
	if err != nil {
		return "", fmt.Errorf("resolve git root: %w", err)
	}
	return resolved, nil
}
