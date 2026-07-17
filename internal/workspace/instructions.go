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

// LoadRootInstructions mirrors the initial Gork Build discovery surface for
// the workspace root. Nested instruction files are intentionally not loaded
// globally because their scope only applies when the agent works below their
// directory.
func (w *Workspace) LoadRootInstructions() ([]InstructionFile, error) {
	var candidates []string
	for _, name := range instructionNames {
		candidates = append(candidates, filepath.Join(w.root, name))
	}
	for _, relativeDir := range rulesDirectories {
		dir := filepath.Join(w.root, relativeDir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read rules directory %q: %w", relativeDir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				candidates = append(candidates, filepath.Join(dir, entry.Name()))
			}
		}
	}
	sort.Strings(candidates)

	seen := make(map[string]struct{})
	var seenFiles []os.FileInfo
	var files []InstructionFile
	total := 0
	for _, candidate := range candidates {
		if IsGitIgnored(w.root, candidate) {
			continue
		}
		if _, err := os.Lstat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction candidate %q: %w", w.Relative(candidate), err)
		}
		resolved, err := w.Resolve(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve instruction file %q: %w", w.Relative(candidate), err)
		}
		if _, duplicate := seen[resolved]; duplicate {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat instruction file %q: %w", w.Relative(resolved), err)
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
			return nil, fmt.Errorf("instruction file %q exceeds %d bytes", w.Relative(resolved), maxInstructionFileBytes)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read instruction file %q: %w", w.Relative(resolved), err)
		}
		total += len(data)
		if total > maxInstructionsBytes {
			return nil, fmt.Errorf("combined project instructions exceed %d bytes", maxInstructionsBytes)
		}
		seen[resolved] = struct{}{}
		seenFiles = append(seenFiles, info)
		files = append(files, InstructionFile{Path: w.Relative(resolved), Content: string(data)})
	}
	return files, nil
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
