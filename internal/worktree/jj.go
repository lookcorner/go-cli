package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func IsJujutsu(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".jj"))
	return err == nil && info.IsDir()
}

func JJInfo(ctx context.Context, root string) (GitInfo, error) {
	resolved, err := jjOutput(ctx, root, false, "workspace", "root")
	if err != nil {
		return GitInfo{}, err
	}
	info := GitInfo{Root: resolved, CurrentBranch: jjBookmarksAt(ctx, root, "@-"), VCSKind: "jujutsuColocated"}
	gitInfo, _ := Info(ctx, root)
	info.Remotes = append(info.Remotes, gitInfo.Remotes...)
	sort.Strings(info.Remotes)
	return info, nil
}

func JJStatus(ctx context.Context, root string) GitStatus {
	status := GitStatus{
		Root:     optionalJJ(ctx, root, "workspace", "root"),
		Commit:   optionalJJ(ctx, root, "log", "--no-graph", "-r", "@", "-T", "commit_id.shortest(12)"),
		Branch:   jjBookmarksAt(ctx, root, "@-"),
		Staged:   []GitFileChange{},
		Unstaged: []GitFileChange{},
	}
	for _, line := range strings.Split(optionalJJ(ctx, root, "diff", "--summary"), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 3 || line[1] != ' ' {
			continue
		}
		kind := map[byte]string{'M': "edit", 'A': "create", 'D': "delete", 'R': "rename"}[line[0]]
		if kind != "" {
			status.Unstaged = append(status.Unstaged, GitFileChange{Path: strings.TrimSpace(line[2:]), Type: kind})
		}
	}
	return status
}

func JJCurrentCommit(ctx context.Context, root string) *string {
	value, err := jjOutput(ctx, root, false, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	if err != nil || value == "" {
		return nil
	}
	return &value
}

func JJBranches(ctx context.Context, root string) (GitBranches, error) {
	resolved, err := jjOutput(ctx, root, false, "workspace", "root")
	if err != nil {
		return GitBranches{}, err
	}
	current := jjBookmarksAt(ctx, root, "@-")
	result := GitBranches{CurrentBranch: current, RepoRoot: resolved, Branches: []GitBranch{}}
	output := optionalJJ(ctx, root, "bookmark", "list", "--all", "-T", `name ++ if(remote, "@" ++ remote, "") ++ "\n"`)
	for _, name := range strings.Split(output, "\n") {
		if name = strings.TrimSpace(name); name != "" {
			remote := strings.Contains(name, "@")
			result.Branches = append(result.Branches, GitBranch{Name: name, Current: !remote && name == current, Remote: remote})
		}
	}
	return result, nil
}

func JJCommit(ctx context.Context, root, message string) (CommitData, error) {
	if _, err := jjOutput(ctx, root, true, "describe", "-m", message); err != nil {
		return CommitData{}, err
	}
	commit := optionalJJ(ctx, root, "log", "--no-graph", "-r", "@", "-T", "commit_id.shortest(12)")
	if _, err := jjOutput(ctx, root, true, "new"); err != nil {
		return CommitData{}, err
	}
	return CommitData{CommitHash: commit, Output: "Commit described and new change started"}, nil
}

func JJDiscard(ctx context.Context, root string, paths []string) error {
	_, err := jjOutput(ctx, root, true, append([]string{"restore"}, paths...)...)
	return err
}

func jjBookmarksAt(ctx context.Context, root, revision string) string {
	return optionalJJ(ctx, root, "log", "--no-graph", "-r", revision, "-T", `bookmarks.join(", ")`)
}

func optionalJJ(ctx context.Context, root string, args ...string) string {
	value, _ := jjOutput(ctx, root, false, args...)
	return value
}

func jjOutput(ctx context.Context, root string, mutable bool, args ...string) (string, error) {
	full := append([]string(nil), args...)
	if !mutable {
		full = append([]string{"--ignore-working-copy"}, full...)
	}
	command := exec.CommandContext(ctx, "jj", full...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("jj %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
