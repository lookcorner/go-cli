package workspace

import (
	"os/exec"
	"path/filepath"
	"strings"
)

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
