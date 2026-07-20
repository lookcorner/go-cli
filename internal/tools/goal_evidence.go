package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	goalEvidenceMaxFiles    = 300
	goalEvidenceMaxFileSize = 64 << 10
)

type goalEvidence struct {
	changesPath  string
	changedFiles []string
}

func (s *GoalStore) ConfigureEvidence(artifactDir string) error {
	if artifactDir == "" {
		return errors.New("goal verifier artifact directory is required")
	}
	if err := ensurePrivateArtifactDir(artifactDir); err != nil {
		return err
	}
	s.mu.Lock()
	s.artifactDir = artifactDir
	s.mu.Unlock()
	return nil
}

func captureGoalBaseline(root string) string {
	if root == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (s *GoalStore) captureEvidence(ctx context.Context, attempt uint32) goalEvidence {
	s.mu.Lock()
	root, artifactDir, baseline, createdAt := s.workspaceRoot, s.artifactDir, s.baselineCommit, s.createdAtUnix
	s.mu.Unlock()
	if root == "" || artifactDir == "" {
		return goalEvidence{}
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if baseline != "" {
		if evidence, ok := captureGoalGitEvidence(runCtx, root, artifactDir, baseline, attempt); ok {
			return evidence
		}
	}
	return captureGoalWalkEvidence(root, artifactDir, createdAt, attempt)
}

func captureGoalGitEvidence(ctx context.Context, root, artifactDir, baseline string, attempt uint32) (goalEvidence, bool) {
	var diff cappedBuffer
	command := exec.CommandContext(ctx, "git", "diff", baseline, "--")
	command.Dir, command.Stdout, command.Stderr = root, &diff, &diff
	if err := command.Run(); err != nil {
		return goalEvidence{}, false
	}
	changed := goalGitPaths(ctx, root, "diff", "--name-only", "-z", baseline, "--")
	changed = append(changed, goalUntrackedFiles(ctx, root)...)
	sort.Strings(changed)
	changed = deduplicateStrings(changed)
	body := append([]byte(nil), diff.data...)
	if diff.truncated {
		body = append(body, []byte("\n... (diff truncated) ...\n")...)
	}
	path := filepath.Join(artifactDir, fmt.Sprintf("goal-classifier-%d.patch", attempt))
	if err := writeGoalArtifact(path, body); err != nil {
		return goalEvidence{changedFiles: changed}, true
	}
	return goalEvidence{changesPath: path, changedFiles: changed}, true
}

func captureGoalWalkEvidence(root, artifactDir string, createdAt int64, attempt uint32) goalEvidence {
	skipped := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, "target": true,
		"dist": true, "build": true, ".gocache": true,
	}
	var diff cappedBuffer
	var changed []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if skipped[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(changed) >= goalEvidenceMaxFiles {
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Unix() < createdAt {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		changed = append(changed, relative)
		if diff.truncated {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		data, readErr := io.ReadAll(io.LimitReader(file, goalEvidenceMaxFileSize+1))
		_ = file.Close()
		if readErr != nil {
			return nil
		}
		safePath := sanitizeGoalEvidencePath(relative)
		fmt.Fprintf(&diff, "diff --git a/%s b/%s\n--- /dev/null\n+++ b/%s\n@@ new file @@\n", safePath, safePath, safePath)
		if bytes.IndexByte(data, 0) >= 0 {
			fmt.Fprintln(&diff, "+(binary content omitted)")
			return nil
		}
		truncated := len(data) > goalEvidenceMaxFileSize
		data = data[:min(len(data), goalEvidenceMaxFileSize)]
		for _, line := range strings.Split(string(data), "\n") {
			fmt.Fprintln(&diff, "+"+line)
		}
		if truncated {
			fmt.Fprintln(&diff, "+... (file truncated) ...")
		}
		return nil
	})
	if len(changed) == 0 {
		return goalEvidence{}
	}
	sort.Strings(changed)
	body := append([]byte(nil), diff.data...)
	if diff.truncated {
		body = append(body, []byte("\n... (diff truncated) ...\n")...)
	}
	path := filepath.Join(artifactDir, fmt.Sprintf("goal-classifier-%d.patch", attempt))
	if writeGoalArtifact(path, body) != nil {
		return goalEvidence{changedFiles: changed}
	}
	return goalEvidence{changesPath: path, changedFiles: changed}
}

func goalUntrackedFiles(ctx context.Context, root string) []string {
	return goalGitPaths(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
}

func goalGitPaths(ctx context.Context, root string, args ...string) []string {
	var output cappedBuffer
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir, command.Stdout, command.Stderr = root, &output, &output
	if command.Run() != nil {
		return nil
	}
	parts := strings.Split(string(output.data), "\x00")
	files := make([]string, 0, min(len(parts), goalEvidenceMaxFiles))
	for _, path := range parts {
		if path != "" {
			files = append(files, path)
			if len(files) == goalEvidenceMaxFiles {
				break
			}
		}
	}
	return files
}

func deduplicateStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func sanitizeGoalEvidencePath(path string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return '\uFFFD'
		}
		return char
	}, path)
}

func (s *GoalStore) writeVerificationDetails(attempt uint32, verdicts []goalVerdict) string {
	s.mu.Lock()
	artifactDir := s.artifactDir
	s.mu.Unlock()
	if artifactDir == "" {
		return ""
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# Goal verification attempt %d\n", attempt)
	for _, verdict := range verdicts {
		fmt.Fprintf(&body, "\n## Skeptic %d: %s\n", verdict.Index+1, verdict.Verdict)
		if gap := strings.TrimSpace(verdict.Gaps); gap != "" {
			body.WriteString("\n")
			body.WriteString(gap)
			body.WriteString("\n")
		}
	}
	path := filepath.Join(artifactDir, fmt.Sprintf("goal-classifier-%d.md", attempt))
	if writeGoalArtifact(path, []byte(body.String())) != nil {
		return ""
	}
	return path
}

func writeGoalArtifact(path string, data []byte) error {
	if err := ensurePrivateArtifactDir(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".goal-verifier-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceStateFile(name, path)
}
