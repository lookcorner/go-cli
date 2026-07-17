package workspace

import (
	"os/exec"
	"path/filepath"
	"strings"
)

func GitRoot(cwd string) string {
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return cwd
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return cwd
	}
	return root
}

// IsGitIgnored delegates ignore semantics to Git, including nested .gitignore
// files and the user's core.excludesFile. Errors mean no ignore decision.
func IsGitIgnored(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	command := exec.Command("git", "check-ignore", "-q", "--no-index", "--", filepath.ToSlash(rel))
	command.Dir = root
	return command.Run() == nil
}
