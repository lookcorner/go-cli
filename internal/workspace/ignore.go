package workspace

import (
	"bytes"
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

// GitIgnored returns the paths ignored by Git using one check-ignore process.
// Errors mean no ignore decision, matching IsGitIgnored.
func GitIgnored(root string, paths []string) map[string]bool {
	ignored := make(map[string]bool)
	if len(paths) == 0 {
		return ignored
	}
	var input bytes.Buffer
	byRelative := make(map[string][]string, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		input.WriteString(rel)
		input.WriteByte(0)
		byRelative[rel] = append(byRelative[rel], path)
	}
	command := exec.Command("git", "check-ignore", "--no-index", "-z", "--stdin")
	command.Dir = root
	command.Stdin = &input
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			return ignored
		}
	}
	for _, raw := range bytes.Split(output, []byte{0}) {
		for _, path := range byRelative[string(raw)] {
			ignored[path] = true
		}
	}
	return ignored
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
