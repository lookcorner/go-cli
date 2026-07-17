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

// LoadInstructions discovers user and project instructions, preserving scope
// order so more specific project rules win.
func (w *Workspace) LoadInstructions() ([]InstructionFile, error) {
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	return w.loadInstructions(home, grokHome)
}

func (w *Workspace) loadInstructions(home, grokHome string) ([]InstructionFile, error) {
	gitRoot := GitRoot(w.root)
	type candidate struct {
		path    string
		project bool
	}
	var scopes []candidate
	if grokHome != "" {
		scopes = append(scopes, candidate{path: grokHome})
	}
	if home != "" {
		scopes = append(scopes,
			candidate{path: filepath.Join(home, ".claude")},
			candidate{path: filepath.Join(home, ".cursor")},
		)
	}
	for _, path := range instructionScopes(gitRoot, w.root) {
		scopes = append(scopes, candidate{path: path, project: true})
	}

	var candidates []candidate
	for _, scope := range scopes {
		var scoped []candidate
		for _, name := range instructionNames {
			scoped = append(scoped, candidate{path: filepath.Join(scope.path, name), project: scope.project})
		}
		for _, relativeDir := range rulesDirectories {
			dir := filepath.Join(scope.path, relativeDir)
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read rules directory %q: %w", instructionPath(gitRoot, dir, scope.project), err)
			}
			var rules []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					rules = append(rules, filepath.Join(dir, entry.Name()))
				}
			}
			sort.Strings(rules)
			for _, rule := range rules {
				scoped = append(scoped, candidate{path: rule, project: scope.project})
			}
		}
		candidates = append(candidates, scoped...)
	}

	seen := make(map[string]struct{})
	var seenFiles []os.FileInfo
	var files []InstructionFile
	total := 0
	for _, candidate := range candidates {
		if candidate.project && IsGitIgnored(gitRoot, candidate.path) {
			continue
		}
		if _, err := os.Lstat(candidate.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction candidate %q: %w", instructionPath(gitRoot, candidate.path, candidate.project), err)
		}
		resolved, err := filepath.EvalSymlinks(candidate.path)
		if err != nil {
			return nil, fmt.Errorf("resolve instruction file %q: %w", instructionPath(gitRoot, candidate.path, candidate.project), err)
		}
		if candidate.project && !pathWithin(gitRoot, resolved) {
			return nil, fmt.Errorf("instruction file %q escapes Git root", displayInstructionPath(gitRoot, candidate.path))
		}
		if _, duplicate := seen[resolved]; duplicate {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction file %q: %w", instructionPath(gitRoot, resolved, candidate.project), err)
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
			return nil, fmt.Errorf("instruction file %q exceeds %d bytes", instructionPath(gitRoot, resolved, candidate.project), maxInstructionFileBytes)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read instruction file %q: %w", instructionPath(gitRoot, resolved, candidate.project), err)
		}
		total += len(data)
		if total > maxInstructionsBytes {
			return nil, fmt.Errorf("combined instructions exceed %d bytes", maxInstructionsBytes)
		}
		seen[resolved] = struct{}{}
		seenFiles = append(seenFiles, info)
		files = append(files, InstructionFile{Path: instructionPath(gitRoot, resolved, candidate.project), Content: string(data)})
	}
	return files, nil
}

func instructionPath(root, path string, project bool) string {
	if project {
		return displayInstructionPath(root, path)
	}
	return filepath.ToSlash(path)
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
