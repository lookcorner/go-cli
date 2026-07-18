package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type GitFileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Type      string `json:"type"`
	Staged    bool   `json:"staged"`
	Additions uint64 `json:"additions"`
	Deletions uint64 `json:"deletions"`
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

func GitRoot(ctx context.Context, cwd string) (string, error) {
	root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(root), err
}

func CurrentCommit(ctx context.Context, root string) (string, error) {
	commit, err := gitOutput(ctx, root, "rev-parse", "HEAD^{commit}")
	return strings.TrimSpace(commit), err
}

func Status(ctx context.Context, root string, includeUntracked, includeStats bool) (GitStatus, error) {
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
	status.Staged, err = gitChanges(ctx, resolved, true, false, includeStats)
	if err != nil {
		return GitStatus{}, err
	}
	status.Unstaged, err = gitChanges(ctx, resolved, false, includeUntracked, includeStats)
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

func gitChanges(ctx context.Context, root string, staged, includeUntracked, includeStats bool) ([]GitFileChange, error) {
	args := []string{"diff", "--name-status", "-z"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	data, err := gitBytes(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	stats := map[string][2]uint64{}
	if includeStats {
		stats, err = gitNumstat(ctx, root, staged)
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
		changes = append(changes, GitFileChange{Path: path, OldPath: oldPath, Type: changeType(code), Staged: staged, Additions: counts[0], Deletions: counts[1]})
	}
	if includeUntracked {
		untracked, err := gitBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		for _, raw := range bytes.Split(untracked, []byte{0}) {
			if len(raw) > 0 {
				changes = append(changes, GitFileChange{Path: filepath.ToSlash(string(raw)), Type: "untracked"})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func gitNumstat(ctx context.Context, root string, staged bool) (map[string][2]uint64, error) {
	args := []string{"diff", "--numstat"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	output, err := gitOutput(ctx, root, args...)
	if err != nil {
		return nil, err
	}
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
	return result, nil
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
