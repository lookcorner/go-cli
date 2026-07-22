package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type FuzzyClientID struct {
	InstanceID string `json:"instanceId"`
	ConnID     string `json:"connId"`
}

type FuzzyRouting struct {
	SessionID      string
	TargetClientID *FuzzyClientID
}

type FuzzyMatch struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Path    string `json:"path"`
	Score   int    `json:"score"`
	Indices []int  `json:"indices"`
}

type FuzzyStatus struct {
	SearchID   string
	Routing    FuzzyRouting
	Matches    []FuzzyMatch
	Total      int
	Done       bool
	Generation uint64
}

type fuzzySearch struct {
	root       string
	hidden     bool
	routing    FuzzyRouting
	updated    time.Time
	generation uint64
	cancel     context.CancelFunc
}

type FuzzySearchManager struct {
	mu       sync.Mutex
	searches map[string]*fuzzySearch
	timeout  time.Duration
	closed   bool
	wg       sync.WaitGroup
}

func NewFuzzySearchManager(timeout time.Duration) *FuzzySearchManager {
	return &FuzzySearchManager{searches: make(map[string]*fuzzySearch), timeout: timeout}
}

func (m *FuzzySearchManager) Open(root string, requestID *string, hidden bool, routing FuzzyRouting) (string, error) {
	ws, err := Open(root)
	if err != nil {
		return "", err
	}
	id := ""
	if requestID != nil {
		id = *requestID
	} else if id, err = newFuzzyID(); err != nil {
		return "", err
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("fuzzy search manager is closed")
	}
	m.cleanupLocked(now)
	if previous := m.searches[id]; previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	m.searches[id] = &fuzzySearch{root: ws.Root(), hidden: hidden, routing: routing, updated: now}
	return id, nil
}

func (m *FuzzySearchManager) Change(ctx context.Context, searchID, query string, dirsOnly bool, limit int, emit func(FuzzyStatus)) bool {
	m.mu.Lock()
	search := m.searches[searchID]
	if search == nil || m.closed {
		m.mu.Unlock()
		return false
	}
	if search.cancel != nil {
		search.cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	search.cancel = cancel
	search.updated = time.Now()
	search.generation++
	generation := search.generation
	root, hidden, routing := search.root, search.hidden, search.routing
	m.wg.Add(1)
	m.mu.Unlock()

	if limit < 0 {
		limit = 0
	}
	go func() {
		defer m.wg.Done()
		matches, total, err := fuzzySearchFiles(ctx, root, query, dirsOnly, limit, hidden)
		if ctx.Err() != nil || !m.current(searchID, generation) {
			return
		}
		if emit != nil {
			if err != nil {
				matches, total = []FuzzyMatch{}, 0
			}
			emit(FuzzyStatus{
				SearchID: searchID, Routing: routing, Matches: matches, Total: total, Done: true, Generation: generation,
			})
		}
	}()
	return true
}

func (m *FuzzySearchManager) Close(searchID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	search := m.searches[searchID]
	if search == nil {
		return false
	}
	if search.cancel != nil {
		search.cancel()
	}
	delete(m.searches, searchID)
	return true
}

func (m *FuzzySearchManager) CloseAll() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, search := range m.searches {
		if search.cancel != nil {
			search.cancel()
		}
	}
	m.searches = make(map[string]*fuzzySearch)
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *FuzzySearchManager) current(searchID string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	search := m.searches[searchID]
	return !m.closed && search != nil && search.generation == generation
}

func (m *FuzzySearchManager) cleanupLocked(now time.Time) {
	if m.timeout <= 0 {
		return
	}
	for id, search := range m.searches {
		if now.Sub(search.updated) <= m.timeout {
			continue
		}
		if search.cancel != nil {
			search.cancel()
		}
		delete(m.searches, id)
	}
}

type fuzzyEntry struct {
	path  string
	isDir bool
}

func fuzzySearchFiles(ctx context.Context, root, query string, dirsOnly bool, limit int, hidden bool) ([]FuzzyMatch, int, error) {
	entries, err := fuzzyEntries(ctx, root, hidden)
	if err != nil {
		return nil, 0, err
	}
	if dirsOnly {
		query = strings.TrimSuffix(query, "/")
	}
	matches := make([]FuzzyMatch, 0)
	for _, entry := range entries {
		if query == "" && strings.Contains(filepath.ToSlash(entry.path), "/") {
			continue
		}
		if dirsOnly && !entry.isDir {
			continue
		}
		score, indices, ok := fuzzyScore(query, filepath.ToSlash(entry.path))
		if !ok {
			continue
		}
		typeName := "file"
		if entry.isDir {
			typeName = "directory"
		}
		matches = append(matches, FuzzyMatch{
			Name: filepath.Base(entry.path), Type: typeName, Path: filepath.Join(root, entry.path), Score: score, Indices: indices,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if query == "" {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if len(matches[i].Path) != len(matches[j].Path) {
			return len(matches[i].Path) < len(matches[j].Path)
		}
		return matches[i].Path < matches[j].Path
	})
	total := len(matches)
	if limit < len(matches) {
		matches = matches[:limit]
	}
	return matches, total, nil
}

func fuzzyEntries(ctx context.Context, root string, hidden bool) ([]fuzzyEntry, error) {
	args := []string{"--files", "--null", "--no-require-git"}
	if hidden {
		args = append(args, "--hidden", "--no-ignore", "--glob", "!.git/**")
	}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("list fuzzy search files: %w", err)
		}
	}
	entries := make(map[string]bool)
	for _, raw := range strings.Split(string(output), "\x00") {
		if raw == "" || !utf8.ValidString(raw) {
			continue
		}
		rel := filepath.Clean(raw)
		if rel == "." || rel == ".git" || strings.HasPrefix(filepath.ToSlash(rel), ".git/") {
			continue
		}
		entries[rel] = false
		for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
			entries[parent] = true
		}
	}
	result := make([]fuzzyEntry, 0, len(entries))
	for path, isDir := range entries {
		result = append(result, fuzzyEntry{path: path, isDir: isDir})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result, nil
}

func fuzzyScore(query, candidate string) (int, []int, bool) {
	if query == "" {
		return 0, []int{}, true
	}
	queryRunes, candidateRunes := []rune(query), []rune(candidate)
	caseSensitive := strings.IndexFunc(query, unicode.IsUpper) >= 0
	if !caseSensitive {
		for index := range queryRunes {
			queryRunes[index] = unicode.ToLower(queryRunes[index])
		}
		for index := range candidateRunes {
			candidateRunes[index] = unicode.ToLower(candidateRunes[index])
		}
	}
	baseStart := 0
	for index, character := range candidateRunes {
		if character == '/' {
			baseStart = index + 1
		}
	}
	bestScore := 0
	var best []int
	for start, character := range candidateRunes {
		if character != queryRunes[0] {
			continue
		}
		indices := []int{start}
		position := start + 1
		for _, wanted := range queryRunes[1:] {
			for position < len(candidateRunes) && candidateRunes[position] != wanted {
				position++
			}
			if position == len(candidateRunes) {
				indices = nil
				break
			}
			indices = append(indices, position)
			position++
		}
		if indices == nil {
			continue
		}
		gaps := indices[len(indices)-1] - indices[0] + 1 - len(indices)
		score := 1000 + len(indices)*100 - gaps*10 - len(candidateRunes)
		if gaps == 0 {
			score += 500
		}
		if indices[0] == 0 {
			score += 300
		}
		if indices[0] == baseStart {
			score += 400
		}
		if score > bestScore {
			bestScore, best = score, indices
		}
	}
	return max(bestScore, 1), best, best != nil
}

func newFuzzyID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate fuzzy search ID: %w", err)
	}
	milliseconds := uint64(time.Now().UnixMilli())
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds)
		milliseconds >>= 8
	}
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
