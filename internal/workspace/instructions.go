package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxInstructionFileBytes = 256 << 10
	maxInstructionsBytes    = 1 << 20
)

var instructionNames = []string{
	"Agents.md",
	"Claude.md",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"AGENT.md",
	"AGENTS.md",
	filepath.Join(".claude", "CLAUDE.md"),
	filepath.Join(".claude", "CLAUDE.local.md"),
}

var rulesDirectories = []string{
	filepath.Join(".gork", "rules"),
	filepath.Join(".claude", "rules"),
	filepath.Join(".cursor", "rules"),
}

type InstructionFile struct {
	Path    string
	Content string
}

// LoadInstructions discovers project instructions from the Git root through
// the current workspace, preserving that order so deeper rules win.
func (w *Workspace) LoadInstructions() ([]InstructionFile, error) {
	gitRoot := GitRoot(w.root)
	var candidates []string
	for _, scope := range instructionScopes(gitRoot, w.root) {
		var scoped []string
		for _, name := range instructionNames {
			scoped = append(scoped, filepath.Join(scope, name))
		}
		for _, relativeDir := range rulesDirectories {
			dir := filepath.Join(scope, relativeDir)
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read rules directory %q: %w", displayInstructionPath(gitRoot, dir), err)
			}
			var rules []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					rules = append(rules, filepath.Join(dir, entry.Name()))
				}
			}
			sort.Strings(rules)
			scoped = append(scoped, rules...)
		}
		candidates = append(candidates, scoped...)
	}

	seen := make(map[string]struct{})
	var seenFiles []os.FileInfo
	var files []InstructionFile
	total := 0
	for _, candidate := range candidates {
		if IsGitIgnored(gitRoot, candidate) {
			continue
		}
		if _, err := os.Lstat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction candidate %q: %w", displayInstructionPath(gitRoot, candidate), err)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve instruction file %q: %w", displayInstructionPath(gitRoot, candidate), err)
		}
		if !pathWithin(gitRoot, resolved) {
			return nil, fmt.Errorf("instruction file %q escapes Git root", displayInstructionPath(gitRoot, candidate))
		}
		if _, duplicate := seen[resolved]; duplicate {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction file %q: %w", displayInstructionPath(gitRoot, resolved), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		duplicateFile := false
		for _, previous := range seenFiles {
			if os.SameFile(previous, info) {
				duplicateFile = true
				break
			}
		}
		if duplicateFile {
			continue
		}
		if info.Size() > maxInstructionFileBytes {
			return nil, fmt.Errorf("instruction file %q exceeds %d bytes", displayInstructionPath(gitRoot, resolved), maxInstructionFileBytes)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read instruction file %q: %w", displayInstructionPath(gitRoot, resolved), err)
		}
		total += len(data)
		if total > maxInstructionsBytes {
			return nil, fmt.Errorf("combined project instructions exceed %d bytes", maxInstructionsBytes)
		}
		seen[resolved] = struct{}{}
		seenFiles = append(seenFiles, info)
		files = append(files, InstructionFile{Path: displayInstructionPath(gitRoot, resolved), Content: string(data)})
	}
	return files, nil
}

func instructionScopes(root, cwd string) []string {
	var reversed []string
	for current := cwd; pathWithin(root, current); current = filepath.Dir(current) {
		reversed = append(reversed, current)
		if current == root {
			break
		}
	}
	scopes := make([]string, len(reversed))
	for index := range reversed {
		scopes[len(reversed)-1-index] = reversed[index]
	}
	return scopes
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func displayInstructionPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func FormatInstructions(files []InstructionFile) string {
	if len(files) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("Project instructions follow. Obey them for files in their scope. More deeply nested instruction files override parent instructions on conflict.\n")
	for _, file := range files {
		fmt.Fprintf(&output, "\n## From: %s\n%s\n", file.Path, stripFrontmatter(file.Content))
	}
	output.WriteString("\nBefore modifying files in a nested directory, check that directory and its ancestors below the workspace for additional AGENTS.md, Claude.md, or rules files.\n")
	return output.String()
}

func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
		return content[4+end+5:]
	}
	return content
}
