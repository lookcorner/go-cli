package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Result struct {
	ChunkID   string
	Path      string
	StartLine int
	EndLine   int
	Score     float64
	Snippet   string
	Source    string
	CreatedAt int64
}

type File struct {
	Path  string
	From  int
	Lines []string
}

type chunk struct {
	path, source, text string
	start, end         int
	created            int64
}

type rankedResult struct {
	raw    float64
	result Result
}

func (s *Store) Search(query string, index IndexConfig, search SearchConfig) ([]Result, error) {
	terms := tokens(query)
	if len(terms) == 0 {
		return nil, errors.New("memory search query is required")
	}
	if index.MaxChunkChars < 1 || index.ChunkOverlapChars < 0 || index.ChunkOverlapChars >= index.MaxChunkChars {
		return nil, errors.New("invalid memory index configuration")
	}
	if search.MaxResults < 1 || search.MinScore < 0 || search.MinScore > 1 {
		return nil, errors.New("invalid memory search configuration")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	var chunks []chunk
	for _, file := range files {
		path, pathErr := s.allowedPath(file.Path)
		if pathErr != nil {
			continue
		}
		data, readErr := readMemoryFile(path)
		if readErr != nil {
			continue
		}
		created := int64(0)
		if file.ModifiedEpochSeconds != nil {
			created = int64(*file.ModifiedEpochSeconds)
		}
		chunks = append(chunks, splitMarkdown(path, file.Source, string(data), created, index)...)
	}
	return rankChunks(chunks, terms, search), nil
}

func (s *Store) Get(path string, from, limit int) (File, error) {
	if from < 0 || limit < 0 {
		return File{}, errors.New("memory line range must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed, err := s.allowedPath(path)
	if err != nil {
		return File{}, err
	}
	data, err := readMemoryFile(allowed)
	if err != nil {
		return File{}, err
	}
	lines := strings.Split(string(data), "\n")
	if from > len(lines) {
		from = len(lines)
	}
	end := len(lines)
	if limit > 0 && from+limit < end {
		end = from + limit
	}
	return File{Path: allowed, From: from, Lines: lines[from:end]}, nil
}

func (s *Store) files() ([]FileInfo, error) {
	files := make([]FileInfo, 0)
	for _, candidate := range []struct{ path, source string }{
		{filepath.Join(s.root, "MEMORY.md"), "global"},
		{filepath.Join(s.workspaceDir, "MEMORY.md"), "workspace"},
	} {
		if info, ok := memoryFileInfo(candidate.path, candidate.source); ok {
			files = append(files, info)
		}
	}
	entries, err := sessionEntries(s.sessionsDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if info, ok := memoryFileInfo(entry.path, "session"); ok {
			files = append(files, info)
		}
	}
	return files, nil
}

func (s *Store) allowedPath(path string) (string, error) {
	for _, dir := range []string{s.root, s.workspaceDir, s.sessionsDir} {
		if err := ensureDirectory(dir); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	root, rootErr := canonicalDirectory(s.root)
	workspace, workspaceErr := canonicalDirectory(s.workspaceDir)
	sessions, sessionsErr := canonicalDirectory(s.sessionsDir)
	if rootErr != nil || workspaceErr != nil || sessionsErr != nil {
		return "", errors.New("memory roots could not be resolved safely")
	}
	if resolved == filepath.Join(root, "MEMORY.md") || resolved == filepath.Join(workspace, "MEMORY.md") {
		return resolved, nil
	}
	rel, err := filepath.Rel(sessions, resolved)
	if err == nil && rel != "." && filepath.Dir(rel) == "." && !strings.HasPrefix(rel, "..") && strings.EqualFold(filepath.Ext(rel), ".md") {
		return resolved, nil
	}
	return "", errors.New("memory path is outside the active memory scope")
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func splitMarkdown(path, source, text string, created int64, cfg IndexConfig) []chunk {
	lines := strings.Split(text, "\n")
	if len([]rune(text)) <= cfg.MaxChunkChars {
		return makeChunk(path, source, created, lines, 0, len(lines), "")
	}
	type section struct {
		start, end int
		context    string
	}
	var sections []section
	start := 0
	parents := map[int]string{}
	context := ""
	for i, line := range lines {
		level := headerLevel(line)
		if level == 0 {
			continue
		}
		if i > start {
			sections = append(sections, section{start, i, context})
		}
		for depth := range parents {
			if depth >= level {
				delete(parents, depth)
			}
		}
		var ancestry []string
		for depth := 1; depth < level; depth++ {
			if heading := parents[depth]; heading != "" {
				ancestry = append(ancestry, heading)
			}
		}
		context = strings.Join(ancestry, " > ")
		parents[level] = strings.TrimSpace(line)
		start = i
	}
	sections = append(sections, section{start, len(lines), context})
	var result []chunk
	for _, section := range sections {
		prefix := ""
		if section.context != "" {
			prefix = "[Context: " + section.context + "]\n\n"
		}
		result = append(result, splitRange(path, source, created, lines, section.start, section.end, prefix, cfg)...)
	}
	return result
}

func splitRange(path, source string, created int64, lines []string, start, end int, prefix string, cfg IndexConfig) []chunk {
	if runeLen(prefix+strings.Join(lines[start:end], "\n")) <= cfg.MaxChunkChars {
		return makeChunk(path, source, created, lines, start, end, prefix)
	}
	var result []chunk
	for pos := start; pos < end; {
		previous := pos
		next := pos
		lastBlank := -1
		for next < end && runeLen(prefix+strings.Join(lines[pos:next+1], "\n")) <= cfg.MaxChunkChars {
			if strings.TrimSpace(lines[next]) == "" && next > pos {
				lastBlank = next
			}
			next++
		}
		if next == pos {
			runes := []rune(lines[pos])
			available := max(1, cfg.MaxChunkChars-runeLen(prefix))
			step := max(1, available-cfg.ChunkOverlapChars)
			for offset := 0; offset < len(runes); offset += step {
				last := min(len(runes), offset+available)
				text := strings.TrimSpace(prefix + string(runes[offset:last]))
				if useful(text) {
					result = append(result, chunk{path: path, source: source, text: text, start: pos, end: pos + 1, created: created})
				}
				if last == len(runes) {
					break
				}
			}
			pos++
			continue
		} else if next < end && lastBlank > pos {
			next = lastBlank
		}
		result = append(result, makeChunk(path, source, created, lines, pos, next, prefix)...)
		if next >= end {
			break
		}
		overlap := 0
		pos = next
		for pos > start && overlap < cfg.ChunkOverlapChars {
			pos--
			overlap += runeLen(lines[pos]) + 1
		}
		if pos <= previous || pos >= next {
			pos = next
		}
	}
	return result
}

func makeChunk(path, source string, created int64, lines []string, start, end int, prefix string) []chunk {
	text := strings.TrimSpace(prefix + strings.Join(lines[start:end], "\n"))
	if !useful(text) {
		return nil
	}
	return []chunk{{path: path, source: source, text: text, start: start, end: end, created: created}}
}

func rankChunks(chunks []chunk, query []string, cfg SearchConfig) []Result {
	docFreq := map[string]int{}
	docs := make([]map[string]int, len(chunks))
	for i, item := range chunks {
		docs[i] = frequencies(tokens(item.text))
		for _, term := range query {
			if docs[i][term] > 0 {
				docFreq[term]++
			}
		}
	}
	type scored struct {
		chunk chunk
		raw   float64
	}
	var matches []scored
	best := 0.0
	for i, item := range chunks {
		raw := 0.0
		for _, term := range query {
			tf := docs[i][term]
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + float64(len(chunks)+1)/float64(docFreq[term]+1))
			raw += idf * float64(tf) / (float64(tf) + 1.2)
		}
		if raw > 0 {
			matches = append(matches, scored{item, raw})
			best = math.Max(best, raw)
		}
	}
	now := time.Now().Unix()
	halfLife := effectiveHalfLife(cfg)
	ranked := make([]rankedResult, 0, len(matches))
	for _, match := range matches {
		rawScore := match.raw / best
		if match.chunk.source == "session" && halfLife > 0 {
			created := max(int64(0), match.chunk.created)
			ageDays := math.Max(0, float64(now-created)/86400)
			rawScore *= math.Exp(-math.Ln2 * ageDays / halfLife)
		}
		weight := 1.0
		if configured, ok := cfg.SourceWeights[match.chunk.source]; ok {
			weight = configured
		}
		rawScore *= weight
		score := min(1, max(0, rawScore))
		if score < cfg.MinScore {
			continue
		}
		id := chunkID(match.chunk.path, match.chunk.start, match.chunk.end)
		ranked = append(ranked, rankedResult{raw: rawScore, result: Result{ChunkID: id, Path: match.chunk.path, StartLine: match.chunk.start, EndLine: match.chunk.end, Score: score, Snippet: match.chunk.text, Source: match.chunk.source, CreatedAt: match.chunk.created}})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].raw != ranked[j].raw {
			return ranked[i].raw > ranked[j].raw
		}
		if ranked[i].result.Path != ranked[j].result.Path {
			return ranked[i].result.Path < ranked[j].result.Path
		}
		return ranked[i].result.StartLine < ranked[j].result.StartLine
	})
	if cfg.MaxResults <= len(ranked)/3 {
		ranked = ranked[:cfg.MaxResults*3]
	}
	if cfg.MMR.Enabled && cfg.MMR.Lambda < 1 && len(ranked) > 1 {
		ranked = rerankMMR(ranked, min(1, max(0, cfg.MMR.Lambda)))
	}
	if len(ranked) > cfg.MaxResults {
		ranked = ranked[:cfg.MaxResults]
	}
	results := make([]Result, len(ranked))
	for index := range ranked {
		results[index] = ranked[index].result
	}
	return results
}

func effectiveHalfLife(cfg SearchConfig) float64 {
	if cfg.TemporalDecay.Enabled {
		return max(0, cfg.TemporalDecay.HalfLifeDays)
	}
	if cfg.RecencyDecay > 0 && cfg.RecencyDecay < 1 && math.Abs(cfg.RecencyDecay-defaultRecencyDecay) > 1e-7 {
		return -1 / math.Log2(cfg.RecencyDecay)
	}
	return 0
}

func rerankMMR(ranked []rankedResult, lambda float64) []rankedResult {
	tokensByResult := make([]map[string]struct{}, len(ranked))
	minScore, maxScore := ranked[0].raw, ranked[0].raw
	for index := range ranked {
		tokensByResult[index] = similarityTokens(ranked[index].result.Snippet)
		minScore = min(minScore, ranked[index].raw)
		maxScore = max(maxScore, ranked[index].raw)
	}
	rangeScore := max(maxScore-minScore, math.SmallestNonzeroFloat64)
	remaining := make([]int, len(ranked))
	for index := range remaining {
		remaining[index] = index
	}
	selected := make([]int, 0, len(ranked))
	for len(remaining) > 0 {
		bestPosition, bestScore := 0, math.Inf(-1)
		for position, candidate := range remaining {
			relevance := (ranked[candidate].raw - minScore) / rangeScore
			similarity := 0.0
			for _, prior := range selected {
				similarity = max(similarity, jaccard(tokensByResult[candidate], tokensByResult[prior]))
			}
			score := lambda*relevance - (1-lambda)*similarity
			if score > bestScore || score == bestScore && ranked[candidate].raw > ranked[remaining[bestPosition]].raw {
				bestPosition, bestScore = position, score
			}
		}
		selected = append(selected, remaining[bestPosition])
		remaining = append(remaining[:bestPosition], remaining[bestPosition+1:]...)
	}
	result := make([]rankedResult, len(ranked))
	for index, selectedIndex := range selected {
		result[index] = ranked[selectedIndex]
	}
	return result
}

func similarityTokens(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		result[token] = struct{}{}
	}
	return result
}

func jaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	intersection := 0
	for token := range left {
		if _, ok := right[token]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
func frequencies(values []string) map[string]int {
	result := map[string]int{}
	for _, value := range values {
		result[value]++
	}
	return result
}
func runeLen(value string) int { return len([]rune(value)) }
func headerLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n > 0 && n <= 6 && n < len(line) && line[n] == ' ' {
		return n
	}
	return 0
}
func useful(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || headerLevel(line) > 0 || strings.HasPrefix(line, "[Context:") {
			continue
		}
		for _, token := range tokens(line) {
			if len([]rune(token)) > 1 {
				return true
			}
		}
	}
	return false
}
func chunkID(path string, start, end int) string {
	sum := sha256String(path + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end))
	return sum[:16]
}
func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
