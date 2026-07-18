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
	Path        string    `json:"path"`
	ID          string    `json:"id"`
	OldStart    int       `json:"oldStart"`
	OldLines    int       `json:"oldLines"`
	NewStart    int       `json:"newStart"`
	NewLines    int       `json:"newLines"`
	Source      string    `json:"source"`
	OldText     string    `json:"oldText,omitempty"`
	NewText     string    `json:"newText"`
	Patch       string    `json:"patch"`
	CreatedAt   time.Time `json:"createdAt"`
	PromptIndex *int      `json:"promptIndex,omitempty"`
}

type HunkFile struct {
	Path        string `json:"path"`
	IsAgentFile bool   `json:"isAgentFile"`
	Staged      bool   `json:"staged"`
	HunkCount   int    `json:"hunkCount"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
}

type HunkSessionStats struct {
	AcceptedHunks        int `json:"acceptedHunks"`
	RejectedHunks        int `json:"rejectedHunks"`
	AcceptedLinesAdded   int `json:"acceptedLinesAdded"`
	AcceptedLinesRemoved int `json:"acceptedLinesRemoved"`
	RejectedLinesAdded   int `json:"rejectedLinesAdded"`
	RejectedLinesRemoved int `json:"rejectedLinesRemoved"`
}

type HunkTurnSummary struct {
	PromptIndex  int      `json:"promptIndex"`
	Files        []string `json:"files"`
	PendingHunks []Hunk   `json:"pendingHunks"`
	LinesAdded   int      `json:"linesAdded"`
	LinesRemoved int      `json:"linesRemoved"`
}

type HunkSessionSummary struct {
	Stats               HunkSessionStats  `json:"stats"`
	Turns               []HunkTurnSummary `json:"turns"`
	FilesModified       int               `json:"filesModified"`
	FilesWithPending    int               `json:"filesWithPending"`
	PendingHunks        int               `json:"pendingHunks"`
	PendingLinesAdded   int               `json:"pendingLinesAdded"`
	PendingLinesRemoved int               `json:"pendingLinesRemoved"`
	UnattributedPending int               `json:"unattributedPending"`
}

type HunkTracker struct {
	ws          *workspace.Workspace
	mu          sync.RWMutex
	agentHunks  map[string]hunkAttribution
	agentFiles  map[string]bool
	promptIndex func() int
	accepted    map[string]bool
	stats       HunkSessionStats
	head        string
	headLogSize int64
	statePath   string
	actionMu    sync.Mutex
}

type hunkAttribution struct {
	createdAt      time.Time
	promptIndex    int
	hasPromptIndex bool
}

func NewHunkTracker(ws *workspace.Workspace) *HunkTracker {
	return &HunkTracker{
		ws: ws, agentHunks: make(map[string]hunkAttribution), agentFiles: make(map[string]bool),
		accepted: make(map[string]bool),
	}
}

func (t *HunkTracker) setPromptIndex(current func() int) {
	t.mu.Lock()
	t.promptIndex = current
	t.mu.Unlock()
}

func (t *HunkTracker) snapshot(ctx context.Context, path string) map[string]bool {
	path, err := t.relativePath(path)
	if err != nil {
		return nil
	}
	hunks, err := t.rawHunks(ctx, path)
	if err != nil {
		return nil
	}
	result := make(map[string]bool, len(hunks))
	for _, hunk := range hunks {
		result[hunkAttributionKey(hunk)] = true
	}
	return result
}

func (t *HunkTracker) markAgentChanges(ctx context.Context, path string, before map[string]bool) {
	if before == nil {
		return
	}
	path, err := t.relativePath(path)
	if err != nil {
		return
	}
	hunks, err := t.rawHunks(ctx, path)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	promptIndex, hasPromptIndex := 0, false
	t.mu.RLock()
	currentPrompt := t.promptIndex
	t.mu.RUnlock()
	if currentPrompt != nil {
		if index := currentPrompt(); index >= 0 {
			promptIndex, hasPromptIndex = index, true
		}
	}
	t.mu.Lock()
	t.agentFiles[path] = true
	for _, hunk := range hunks {
		key := hunkAttributionKey(hunk)
		if !before[key] {
			t.agentHunks[key] = hunkAttribution{createdAt: now, promptIndex: promptIndex, hasPromptIndex: hasPromptIndex}
		}
	}
	t.mu.Unlock()
}

func (t *HunkTracker) Hunks(ctx context.Context, path, source string) ([]Hunk, error) {
	if t == nil || t.ws == nil {
		return nil, errors.New("hunk tracker unavailable")
	}
	if path != "" {
		var err error
		path, err = t.entryRelativePath(path)
		if err != nil {
			return nil, err
		}
	}
	hunks, err := t.rawHunks(ctx, path)
	if err != nil {
		return nil, err
	}
	unique := make(map[string]Hunk, len(hunks))
	for _, hunk := range hunks {
		unique[hunk.ID] = hunk
	}
	hunks = hunks[:0]
	for _, hunk := range unique {
		if !t.isAccepted(hunk.ID) {
			hunks = append(hunks, hunk)
		}
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

func (t *HunkTracker) relativePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	resolved, err := t.ws.Resolve(path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(t.ws.Relative(resolved)), nil
}

func (t *HunkTracker) entryRelativePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	resolved, err := t.ws.ResolveEntry(path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(t.ws.Relative(resolved)), nil
}

func (t *HunkTracker) rawHunks(ctx context.Context, path string) ([]Hunk, error) {
	t.syncHead(ctx)
	unstaged, err := t.gitDiff(ctx, false, path)
	if err != nil {
		return nil, err
	}
	staged, err := t.gitDiff(ctx, true, path)
	if err != nil {
		return nil, err
	}
	hunks := append(t.parseDiff(unstaged), t.parseDiff(staged)...)
	untracked, err := t.untracked(ctx, path)
	if err != nil {
		return nil, err
	}
	hunks = append(hunks, untracked...)
	t.mu.Lock()
	for _, hunk := range hunks {
		if hunk.Source == "agent" {
			t.agentFiles[hunk.Path] = true
		}
	}
	t.mu.Unlock()
	return hunks, nil
}

// HunkAction accepts or rejects one currently visible hunk. Accepting hides
// the hunk for this tracker session. Rejecting first verifies the exact text
// at the recorded line range, then restores the old text atomically.
func (t *HunkTracker) HunkAction(ctx context.Context, id, action string) (int, error) {
	if strings.TrimSpace(id) == "" {
		return 0, errors.New("hunkId is required")
	}
	hunks, err := t.Hunks(ctx, "", "all")
	if err != nil {
		return 0, err
	}
	for _, hunk := range hunks {
		if hunk.ID == id {
			return t.applyAction([]Hunk{hunk}, action)
		}
	}
	return 0, fmt.Errorf("unknown or already accepted hunk %q", id)
}

// FileAction applies an action to every currently visible hunk for path.
func (t *HunkTracker) FileAction(ctx context.Context, path, action string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, errors.New("path is required")
	}
	hunks, err := t.Hunks(ctx, path, "all")
	if err != nil {
		return 0, err
	}
	if len(hunks) == 0 {
		return 0, fmt.Errorf("no visible hunks for %q", path)
	}
	return t.applyAction(hunks, action)
}

// AllAction applies an action to every currently visible hunk.
func (t *HunkTracker) AllAction(ctx context.Context, action string) (int, error) {
	hunks, err := t.Hunks(ctx, "", "all")
	if err != nil {
		return 0, err
	}
	if len(hunks) == 0 {
		return 0, errors.New("no visible hunks")
	}
	return t.applyAction(hunks, action)
}

func (t *HunkTracker) TurnAction(ctx context.Context, promptIndex int, action string) (int, error) {
	if promptIndex < 0 {
		return 0, errors.New("promptIndex must be non-negative")
	}
	hunks, err := t.Hunks(ctx, "", "agent")
	if err != nil {
		return 0, err
	}
	selected := hunks[:0]
	for _, hunk := range hunks {
		if hunk.PromptIndex != nil && *hunk.PromptIndex == promptIndex {
			selected = append(selected, hunk)
		}
	}
	if len(selected) == 0 {
		return 0, fmt.Errorf("no visible agent hunks for promptIndex %d", promptIndex)
	}
	return t.applyAction(selected, action)
}

func (t *HunkTracker) applyAction(hunks []Hunk, action string) (int, error) {
	if action != "accept" && action != "reject" {
		return 0, errors.New("action must be accept or reject")
	}
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	if action == "accept" {
		t.markHandled(hunks, action)
		return len(hunks), nil
	}

	byPath := make(map[string][]Hunk)
	for _, hunk := range hunks {
		byPath[hunk.Path] = append(byPath[hunk.Path], hunk)
	}
	type plannedWrite struct {
		path     string
		original []byte
		updated  []byte
		mode     os.FileMode
		remove   bool
	}
	plans := make([]plannedWrite, 0, len(byPath))
	for name, fileHunks := range byPath {
		resolved, err := t.ws.Resolve(name)
		if err != nil {
			return 0, err
		}
		data, mode, missing, err := readForReject(resolved)
		if err != nil {
			return 0, err
		}
		sort.Slice(fileHunks, func(i, j int) bool { return fileHunks[i].NewStart > fileHunks[j].NewStart })
		updated := string(data)
		for _, hunk := range fileHunks {
			updated, err = rejectText(updated, hunk)
			if err != nil {
				return 0, fmt.Errorf("reject %s hunk %s: %w", name, hunk.ID, err)
			}
		}
		remove := !missing && updated == "" && len(fileHunks) == 1 && fileHunks[0].OldLines == 0 && string(data) == fileHunks[0].NewText
		plans = append(plans, plannedWrite{path: resolved, original: data, updated: []byte(updated), mode: mode, remove: remove})
	}
	// Recheck every file after planning so a concurrent edit fails closed.
	for _, plan := range plans {
		current, _, _, err := readForReject(plan.path)
		if err != nil {
			return 0, err
		}
		if string(current) != string(plan.original) {
			return 0, fmt.Errorf("%q changed while reject was being prepared", t.ws.Relative(plan.path))
		}
	}
	for _, plan := range plans {
		if plan.remove {
			if err := os.Remove(plan.path); err != nil {
				return 0, fmt.Errorf("remove rejected file %q: %w", t.ws.Relative(plan.path), err)
			}
			continue
		}
		if err := atomicWrite(plan.path, plan.updated, plan.mode); err != nil {
			return 0, err
		}
	}
	t.markHandled(hunks, action)
	return len(hunks), nil
}

func readForReject(path string) ([]byte, os.FileMode, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0o644, true, nil
		}
		return nil, 0, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%q is not a regular file", path)
	}
	return data, info.Mode().Perm(), false, nil
}

func rejectText(current string, hunk Hunk) (string, error) {
	lines := strings.SplitAfter(current, "\n")
	start := hunk.NewStart - 1
	if hunk.NewLines == 0 {
		start = hunk.NewStart
	}
	if start < 0 {
		start = 0
	}
	end := start + hunk.NewLines
	if start > len(lines) || end > len(lines) {
		return "", errors.New("recorded line range no longer exists")
	}
	if got := strings.Join(lines[start:end], ""); got != hunk.NewText {
		return "", errors.New("current text does not exactly match the hunk")
	}
	return strings.Join(lines[:start], "") + hunk.OldText + strings.Join(lines[end:], ""), nil
}

func (t *HunkTracker) isAccepted(id string) bool {
	t.mu.RLock()
	accepted := t.accepted[id]
	t.mu.RUnlock()
	return accepted
}

func (t *HunkTracker) markHandled(hunks []Hunk, action string) {
	t.mu.Lock()
	for _, hunk := range hunks {
		t.accepted[hunk.ID] = true
		if action == "accept" {
			t.stats.AcceptedHunks++
			t.stats.AcceptedLinesAdded += hunk.NewLines
			t.stats.AcceptedLinesRemoved += hunk.OldLines
		} else {
			t.stats.RejectedHunks++
			t.stats.RejectedLinesAdded += hunk.NewLines
			t.stats.RejectedLinesRemoved += hunk.OldLines
		}
	}
	t.mu.Unlock()
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

func (t *HunkTracker) IsAgentFile(path string) bool {
	t.mu.RLock()
	tracked := t.agentFiles[path]
	t.mu.RUnlock()
	return tracked
}

func (t *HunkTracker) syncHead(ctx context.Context) {
	head, headLogSize := t.currentGitIdentity(ctx)
	if head == "" {
		return
	}
	t.mu.Lock()
	if gitIdentityChanged(t.head, t.headLogSize, head, headLogSize) {
		t.agentHunks = make(map[string]hunkAttribution)
		t.accepted = make(map[string]bool)
	}
	t.head = head
	t.headLogSize = headLogSize
	t.mu.Unlock()
}

func gitIdentityChanged(oldHead string, oldLogSize int64, head string, logSize int64) bool {
	return oldHead != "" && (oldHead != head || oldLogSize != 0 && logSize != 0 && oldLogSize != logSize)
}

func (t *HunkTracker) currentGitIdentity(ctx context.Context) (string, int64) {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	command.Dir = t.ws.Root()
	output, err := command.Output()
	if err != nil {
		return "", 0
	}
	head := strings.TrimSpace(string(output))
	logCommand := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "logs/HEAD")
	logCommand.Dir = t.ws.Root()
	logOutput, err := logCommand.Output()
	if err != nil {
		return head, 0
	}
	logPath := strings.TrimSpace(string(logOutput))
	if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(t.ws.Root(), logPath)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		return head, 0
	}
	return head, info.Size()
}

func (t *HunkTracker) Summary(ctx context.Context) (HunkSessionSummary, error) {
	if t == nil {
		return HunkSessionSummary{}, errors.New("hunk tracker unavailable")
	}
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	hunks, err := t.Hunks(ctx, "", "all")
	if err != nil {
		return HunkSessionSummary{}, err
	}
	t.mu.RLock()
	summary := HunkSessionSummary{Stats: t.stats, Turns: []HunkTurnSummary{}}
	t.mu.RUnlock()
	turns := make(map[int]*HunkTurnSummary)
	files := make(map[string]bool)
	for _, hunk := range hunks {
		if hunk.Source != "agent" || hunk.PromptIndex == nil {
			summary.UnattributedPending++
			continue
		}
		turn := turns[*hunk.PromptIndex]
		if turn == nil {
			turn = &HunkTurnSummary{PromptIndex: *hunk.PromptIndex}
			turns[*hunk.PromptIndex] = turn
		}
		if !containsPath(turn.Files, hunk.Path) {
			turn.Files = append(turn.Files, hunk.Path)
		}
		turn.PendingHunks = append(turn.PendingHunks, hunk)
		turn.LinesAdded += hunk.NewLines
		turn.LinesRemoved += hunk.OldLines
		files[hunk.Path] = true
		summary.PendingHunks++
		summary.PendingLinesAdded += hunk.NewLines
		summary.PendingLinesRemoved += hunk.OldLines
	}
	for _, turn := range turns {
		sort.Strings(turn.Files)
		summary.Turns = append(summary.Turns, *turn)
	}
	sort.Slice(summary.Turns, func(i, j int) bool { return summary.Turns[i].PromptIndex < summary.Turns[j].PromptIndex })
	summary.FilesModified = len(files)
	summary.FilesWithPending = len(files)
	return summary, nil
}

func containsPath(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func (t *HunkTracker) parseDiff(diff string) []Hunk {
	scanner := bufio.NewScanner(strings.NewReader(diff))
	var path string
	var current *Hunk
	var patch, oldText, newText strings.Builder
	var previous byte
	var result []Hunk
	flush := func() {
		if current == nil {
			return
		}
		current.Patch, current.OldText, current.NewText = patch.String(), oldText.String(), newText.String()
		current.Source, current.CreatedAt, current.PromptIndex = t.source(*current)
		sum := sha256.Sum256([]byte(path + current.Patch))
		current.ID = hex.EncodeToString(sum[:12])
		result = append(result, *current)
		current = nil
		patch.Reset()
		oldText.Reset()
		newText.Reset()
		previous = 0
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			continue
		}
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
		if line == `\ No newline at end of file` {
			if previous == '-' {
				trimBuilderNewline(&oldText)
			} else if previous == '+' {
				trimBuilderNewline(&newText)
			}
			continue
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			oldText.WriteString(strings.TrimPrefix(line, "-") + "\n")
			previous = '-'
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			newText.WriteString(strings.TrimPrefix(line, "+") + "\n")
			previous = '+'
		}
	}
	flush()
	return result
}

func trimBuilderNewline(builder *strings.Builder) {
	value := builder.String()
	if strings.HasSuffix(value, "\n") {
		builder.Reset()
		builder.WriteString(strings.TrimSuffix(value, "\n"))
	}
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
		patch := fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount(text))
		hunk := Hunk{Path: filepath.ToSlash(name), NewStart: 1, NewLines: lineCount(text), NewText: text, Patch: patch + addPrefixes(text)}
		source, created, promptIndex := t.source(hunk)
		sum := sha256.Sum256([]byte(name + patch + text))
		hunk.ID, hunk.Source, hunk.CreatedAt, hunk.PromptIndex = hex.EncodeToString(sum[:12]), source, created, promptIndex
		result = append(result, hunk)
	}
	return result, nil
}

func (t *HunkTracker) source(hunk Hunk) (string, time.Time, *int) {
	t.mu.RLock()
	attribution, ok := t.agentHunks[hunkAttributionKey(hunk)]
	t.mu.RUnlock()
	if ok {
		if attribution.hasPromptIndex {
			index := attribution.promptIndex
			return "agent", attribution.createdAt, &index
		}
		return "agent", attribution.createdAt, nil
	}
	return "external", time.Time{}, nil
}

func hunkAttributionKey(hunk Hunk) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(hunk.Path)) + "\x00" + hunk.OldText + "\x00" + hunk.NewText))
	return hex.EncodeToString(sum[:])
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
