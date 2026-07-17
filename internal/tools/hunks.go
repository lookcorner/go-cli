package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type Hunk struct {
	Path      string    `json:"path"`
	ID        string    `json:"id"`
	OldStart  int       `json:"oldStart"`
	OldLines  int       `json:"oldLines"`
	NewStart  int       `json:"newStart"`
	NewLines  int       `json:"newLines"`
	Source    string    `json:"source"`
	OldText   string    `json:"oldText,omitempty"`
	NewText   string    `json:"newText"`
	Patch     string    `json:"patch"`
	CreatedAt time.Time `json:"createdAt"`
}

type HunkFile struct {
	Path        string `json:"path"`
	IsAgentFile bool   `json:"isAgentFile"`
	Staged      bool   `json:"staged"`
	HunkCount   int    `json:"hunkCount"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
}

type HunkTracker struct {
	ws         *workspace.Workspace
	mu         sync.RWMutex
	agentPaths map[string]time.Time
}

func NewHunkTracker(ws *workspace.Workspace) *HunkTracker {
	return &HunkTracker{ws: ws, agentPaths: make(map[string]time.Time)}
}

func (t *HunkTracker) MarkAgent(path string) {
	if path == "" {
		return
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	t.mu.Lock()
	t.agentPaths[clean] = time.Now().UTC()
	t.mu.Unlock()
}

func (t *HunkTracker) Hunks(ctx context.Context, path, source string) ([]Hunk, error) {
	if t == nil || t.ws == nil {
		return nil, errors.New("hunk tracker unavailable")
	}
	if path != "" {
		resolved, err := t.ws.Resolve(path)
		if err != nil {
			return nil, err
		}
		path, err = t.ws.Relative(resolved), nil
		if err != nil {
			return nil, err
		}
	}
	unstaged, err := t.gitDiff(ctx, false, path)
	if err != nil {
		return nil, err
	}
	stagedDiff, err := t.gitDiff(ctx, true, path)
	if err != nil {
		return nil, err
	}
	hunks := append(t.parseDiff(unstaged), t.parseDiff(stagedDiff)...)
	untracked, err := t.untracked(ctx, path)
	if err != nil {
		return nil, err
	}
	hunks = append(hunks, untracked...)
	unique := make(map[string]Hunk, len(hunks))
	for _, hunk := range hunks {
		unique[hunk.ID] = hunk
	}
	hunks = hunks[:0]
	for _, hunk := range unique {
		hunks = append(hunks, hunk)
	}
	if source != "" && source != "all" {
		filtered := hunks[:0]
		for _, hunk := range hunks {
			if hunk.Source == source {
				filtered = append(filtered, hunk)
			}
		}
		hunks = filtered
	}
	sort.Slice(hunks, func(i, j int) bool {
		if hunks[i].Path == hunks[j].Path {
			return hunks[i].NewStart < hunks[j].NewStart
		}
		return hunks[i].Path < hunks[j].Path
	})
	return hunks, nil
}

func (t *HunkTracker) gitDiff(ctx context.Context, cached bool, path string) (string, error) {
	args := []string{"diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--no-ext-diff", "--no-color", "--unified=0", "--")
	if path != "" {
		args = append(args, path)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = t.ws.Root()
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", fmt.Errorf("git diff: %w", err)
		}
	}
	return string(output), nil
}

func (t *HunkTracker) Files(ctx context.Context) ([]HunkFile, error) {
	hunks, err := t.Hunks(ctx, "", "all")
	if err != nil {
		return nil, err
	}
	staged := t.stagedPaths(ctx)
	byPath := make(map[string]*HunkFile)
	for _, hunk := range hunks {
		item := byPath[hunk.Path]
		if item == nil {
			item = &HunkFile{Path: hunk.Path, Staged: staged[hunk.Path]}
			byPath[hunk.Path] = item
		}
		item.HunkCount++
		item.Additions += hunk.NewLines
		item.Deletions += hunk.OldLines
		item.IsAgentFile = item.IsAgentFile || hunk.Source == "agent"
	}
	files := make([]HunkFile, 0, len(byPath))
	for _, item := range byPath {
		files = append(files, *item)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (t *HunkTracker) parseDiff(diff string) []Hunk {
	scanner := bufio.NewScanner(strings.NewReader(diff))
	var path string
	var current *Hunk
	var patch, oldText, newText strings.Builder
	var result []Hunk
	flush := func() {
		if current == nil {
			return
		}
		current.Patch, current.OldText, current.NewText = patch.String(), oldText.String(), newText.String()
		current.Source, current.CreatedAt = t.source(path)
		sum := sha256.Sum256([]byte(path + current.Patch))
		current.ID = hex.EncodeToString(sum[:12])
		result = append(result, *current)
		current = nil
		patch.Reset()
		oldText.Reset()
		newText.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "--- a/") {
			path = strings.TrimPrefix(line, "--- a/")
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			path = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if matches := hunkHeader.FindStringSubmatch(line); matches != nil {
			flush()
			current = &Hunk{Path: path, OldStart: atoi(matches[1]), OldLines: hunkCount(matches[2]), NewStart: atoi(matches[3]), NewLines: hunkCount(matches[4])}
			patch.WriteString(line + "\n")
			continue
		}
		if current == nil {
			continue
		}
		patch.WriteString(line + "\n")
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			oldText.WriteString(strings.TrimPrefix(line, "-") + "\n")
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			newText.WriteString(strings.TrimPrefix(line, "+") + "\n")
		}
	}
	flush()
	return result
}

func (t *HunkTracker) untracked(ctx context.Context, path string) ([]Hunk, error) {
	args := []string{"ls-files", "--others", "--exclude-standard", "--"}
	if path != "" {
		args = append(args, path)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = t.ws.Root()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list untracked files: %w", err)
	}
	var result []Hunk
	for _, name := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if name == "" {
			continue
		}
		resolved, err := t.ws.Resolve(name)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil || strings.IndexByte(string(data), 0) >= 0 {
			continue
		}
		text := string(data)
		source, created := t.source(filepath.ToSlash(name))
		patch := fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount(text))
		sum := sha256.Sum256([]byte(name + patch + text))
		result = append(result, Hunk{Path: filepath.ToSlash(name), ID: hex.EncodeToString(sum[:12]), NewStart: 1, NewLines: lineCount(text), Source: source, NewText: text, Patch: patch + addPrefixes(text), CreatedAt: created})
	}
	return result, nil
}

func (t *HunkTracker) source(path string) (string, time.Time) {
	t.mu.RLock()
	created, ok := t.agentPaths[filepath.ToSlash(filepath.Clean(path))]
	t.mu.RUnlock()
	if ok {
		return "agent", created
	}
	return "external", time.Time{}
}

func (t *HunkTracker) stagedPaths(ctx context.Context) map[string]bool {
	command := exec.CommandContext(ctx, "git", "diff", "--cached", "--name-only", "--")
	command.Dir = t.ws.Root()
	output, _ := command.Output()
	result := make(map[string]bool)
	for _, path := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if path != "" {
			result[filepath.ToSlash(path)] = true
		}
	}
	return result
}

func atoi(value string) int { result, _ := strconv.Atoi(value); return result }
func hunkCount(value string) int {
	if value == "" {
		return 1
	}
	return atoi(value)
}
func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + boolInt(!strings.HasSuffix(value, "\n"))
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func addPrefixes(value string) string {
	var result strings.Builder
	for _, line := range strings.SplitAfter(value, "\n") {
		if line != "" {
			result.WriteString("+" + line)
		}
	}
	return result.String()
}
