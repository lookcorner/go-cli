package session

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	foreignMaxAge   = 30 * 24 * time.Hour
	foreignMaxFiles = 128
	foreignMaxItems = 50
	foreignMaxHead  = 4 << 20
)

var foreignUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ForeignSources struct {
	Claude bool
	Codex  bool
}

type ForeignSummary struct {
	ID        string
	Source    string
	Title     string
	CWD       string
	Branch    string
	UpdatedAt time.Time
}

type RecentForeignSession struct {
	ForeignSummary
	Age time.Duration
}

func ForeignSummaries(cwd string, enabled ForeignSources) []ForeignSummary {
	if strings.TrimSpace(cwd) == "" {
		return nil
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err == nil {
		cwd = canonical
	}
	now := time.Now()
	var summaries []ForeignSummary
	if enabled.Claude {
		summaries = append(summaries, scanClaude(cwd, now)...)
	}
	if enabled.Codex {
		summaries = append(summaries, scanCodex(cwd, now)...)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			if summaries[i].Source == summaries[j].Source {
				return summaries[i].ID < summaries[j].ID
			}
			return summaries[i].Source < summaries[j].Source
		}
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries
}

func MostRecentForeignSession(cwd string, enabled ForeignSources, within time.Duration) *RecentForeignSession {
	now := time.Now()
	for _, item := range ForeignSummaries(cwd, enabled) {
		age := now.Sub(item.UpdatedAt)
		if age < 0 {
			age = 0
		}
		if age <= within {
			return &RecentForeignSession{ForeignSummary: item, Age: age}
		}
	}
	return nil
}

func scanClaude(cwd string, now time.Time) []ForeignSummary {
	home := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".claude")
		}
	}
	projectName := sanitizeClaudeProject(cwd)
	if projectName == "" {
		return nil
	}
	project := filepath.Join(home, "projects", projectName)
	if !safeForeignDir(project) {
		return nil
	}
	entries, err := os.ReadDir(project)
	if err != nil {
		return nil
	}
	candidates := make([]foreignCandidate, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".jsonl" || !foreignUUID.MatchString(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || !foreignRecent(info.ModTime(), now) {
			continue
		}
		candidates = append(candidates, foreignCandidate{path: filepath.Join(project, entry.Name()), id: id, updated: info.ModTime(), size: info.Size()})
	}
	sortCandidates(candidates)
	return readForeignCandidates(candidates, func(candidate foreignCandidate) (ForeignSummary, bool) {
		return readClaude(candidate, cwd)
	})
}

func scanCodex(cwd string, now time.Time) []ForeignSummary {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".codex")
		}
	}
	if summaries := scanCodexDatabases(home, cwd, now); len(summaries) > 0 {
		return summaries
	}
	return scanCodexFiles(home, cwd, now)
}

func scanCodexFiles(home, cwd string, now time.Time) []ForeignSummary {
	root := filepath.Join(home, "sessions")
	if !safeForeignDir(root) {
		return nil
	}
	var candidates []foreignCandidate
	seenDates := make(map[string]bool)
	for days := 0; days < 31; days++ {
		for _, current := range []time.Time{now.UTC().AddDate(0, 0, -days), now.Local().AddDate(0, 0, -days)} {
			date := current.Format("2006/01/02")
			if seenDates[date] {
				continue
			}
			seenDates[date] = true
			dateDir := filepath.Join(root, filepath.FromSlash(date))
			if !safeForeignDir(dateDir) {
				continue
			}
			entries, err := os.ReadDir(dateDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				id := codexRolloutID(entry.Name())
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || id == "" {
					continue
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || !foreignRecent(info.ModTime(), now) {
					continue
				}
				candidates = append(candidates, foreignCandidate{path: filepath.Join(dateDir, entry.Name()), id: id, updated: info.ModTime(), size: info.Size()})
			}
		}
	}
	sortCandidates(candidates)
	return readForeignCandidates(candidates, func(candidate foreignCandidate) (ForeignSummary, bool) {
		return readCodex(candidate, cwd)
	})
}

type foreignCandidate struct {
	path    string
	id      string
	updated time.Time
	size    int64
}

func sortCandidates(candidates []foreignCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].updated.Equal(candidates[j].updated) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].updated.After(candidates[j].updated)
	})
}

func readForeignCandidates(candidates []foreignCandidate, read func(foreignCandidate) (ForeignSummary, bool)) []ForeignSummary {
	seen := make(map[string]bool)
	items := make([]ForeignSummary, 0, min(len(candidates), foreignMaxItems))
	for index, candidate := range candidates {
		if index >= foreignMaxFiles || seen[candidate.id] {
			continue
		}
		item, ok := read(candidate)
		if !ok {
			continue
		}
		seen[candidate.id] = true
		items = append(items, item)
		if len(items) == foreignMaxItems {
			break
		}
	}
	return items
}

func readClaude(candidate foreignCandidate, cwd string) (ForeignSummary, bool) {
	head, ok := readFilePart(candidate.path, 0, min(candidate.size, foreignMaxHead))
	if !ok {
		return ForeignSummary{}, false
	}
	lines := strings.Split(head, "\n")
	if len(lines) > 0 && strings.Contains(lines[0], `"isSidechain"`) {
		var first map[string]any
		if json.Unmarshal([]byte(lines[0]), &first) == nil && first["isSidechain"] == true {
			return ForeignSummary{}, false
		}
	}
	storedCWD := firstJSONText(lines, "cwd")
	if storedCWD != cwd {
		return ForeignSummary{}, false
	}
	tail, _ := readFilePart(candidate.path, max(candidate.size-(64<<10), 0), 64<<10)
	allTail := strings.Split(tail, "\n")
	title := firstNonEmpty(
		lastJSONText(allTail, "customTitle"), lastJSONText(lines, "customTitle"),
		lastJSONText(allTail, "aiTitle"), lastJSONText(lines, "aiTitle"),
		lastJSONText(allTail, "lastPrompt"), lastJSONText(lines, "lastPrompt"),
		lastJSONText(allTail, "summary"), lastJSONText(lines, "summary"),
		claudeFirstPrompt(lines),
	)
	if title == "" {
		return ForeignSummary{}, false
	}
	return ForeignSummary{ID: candidate.id, Source: "claude", Title: title, CWD: storedCWD, Branch: normalizeForeignTitle(firstNonEmpty(lastJSONText(allTail, "gitBranch"), lastJSONText(lines, "gitBranch"))), UpdatedAt: candidate.updated.UTC()}, true
}

func readCodex(candidate foreignCandidate, cwd string) (ForeignSummary, bool) {
	if !safeForeignFile(candidate.path) {
		return ForeignSummary{}, false
	}
	file, err := os.Open(candidate.path)
	if err != nil {
		return ForeignSummary{}, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	var id, storedCWD, source, branch, title string
	for records := 0; records < 10 && scanner.Scan(); records++ {
		var record struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.Type == "session_meta" && id == "" {
			id, _ = record.Payload["id"].(string)
			storedCWD, _ = record.Payload["cwd"].(string)
			source = codexSource(record.Payload["source"])
			if git, ok := record.Payload["git"].(map[string]any); ok {
				branch, _ = git["branch"].(string)
			}
			if branch == "" {
				branch, _ = record.Payload["git_branch"].(string)
			}
		}
		if title == "" {
			title = codexUserMessage(record.Payload)
		}
	}
	if id != candidate.id || storedCWD != cwd || source == "" || title == "" {
		return ForeignSummary{}, false
	}
	return ForeignSummary{ID: id, Source: "codex", Title: normalizeForeignTitle(title), CWD: storedCWD, Branch: normalizeForeignTitle(branch), UpdatedAt: candidate.updated.UTC()}, true
}

func readFilePart(path string, offset, limit int64) (string, bool) {
	if !safeForeignFile(path) {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	return string(data), err == nil
}

func firstJSONText(lines []string, key string) string {
	for _, line := range lines {
		if value := jsonText(line, key); value != "" {
			return value
		}
	}
	return ""
}

func lastJSONText(lines []string, key string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		if value := jsonText(lines[index], key); value != "" {
			return value
		}
	}
	return ""
}

func jsonText(line, key string) string {
	var value map[string]any
	if json.Unmarshal([]byte(line), &value) != nil {
		return ""
	}
	text, _ := value[key].(string)
	return text
}

func claudeFirstPrompt(lines []string) string {
	for _, line := range lines {
		var record struct {
			Type             string `json:"type"`
			IsMeta           bool   `json:"isMeta"`
			IsCompactSummary bool   `json:"isCompactSummary"`
			Message          struct {
				Content any `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.Type != "user" || record.IsMeta || record.IsCompactSummary {
			continue
		}
		var texts []string
		switch content := record.Message.Content.(type) {
		case string:
			texts = append(texts, content)
		case []any:
			for _, raw := range content {
				block, _ := raw.(map[string]any)
				if block["type"] == "text" {
					text, _ := block["text"].(string)
					texts = append(texts, text)
				}
			}
		}
		for _, text := range texts {
			text = normalizeForeignTitle(text)
			if text != "" && !strings.HasPrefix(text, "<environment_context>") && !strings.HasPrefix(text, "<user_instructions>") {
				return text
			}
		}
	}
	return ""
}

func codexUserMessage(payload map[string]any) string {
	typeName, _ := payload["type"].(string)
	if typeName == "user_message" {
		text, _ := payload["message"].(string)
		return text
	}
	role, _ := payload["role"].(string)
	if typeName != "message" || role != "user" {
		return ""
	}
	content, _ := payload["content"].([]any)
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		if block["type"] == "input_text" || block["type"] == "text" {
			text, _ := block["text"].(string)
			trimmed := strings.TrimSpace(text)
			if !strings.HasPrefix(trimmed, "<environment_context>") && !strings.HasPrefix(trimmed, "<user_instructions>") {
				return text
			}
		}
	}
	return ""
}

func codexSource(value any) string {
	if text, ok := value.(string); ok && (text == "cli" || text == "vscode") {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		if custom, _ := object["custom"].(string); custom == "atlas" || custom == "chatgpt" {
			return custom
		}
	}
	return ""
}

func codexRolloutID(name string) string {
	if !strings.HasPrefix(name, "rollout-") || filepath.Ext(name) != ".jsonl" {
		return ""
	}
	stem := strings.TrimSuffix(name, ".jsonl")
	if len(stem) < 36 {
		return ""
	}
	id := stem[len(stem)-36:]
	if !foreignUUID.MatchString(id) {
		return ""
	}
	return id
}

func sanitizeClaudeProject(path string) string {
	var output strings.Builder
	for _, char := range path {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			output.WriteRune(char)
		} else {
			output.WriteByte('-')
		}
	}
	if output.Len() > 200 {
		return ""
	}
	return output.String()
}

func foreignRecent(updated, now time.Time) bool {
	age := now.Sub(updated)
	return age <= foreignMaxAge && age >= -5*time.Minute
}

func normalizeForeignTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 200 {
		value = string(runes[:199]) + "…"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = normalizeForeignTitle(value); value != "" {
			return value
		}
	}
	return ""
}

func safeForeignDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func safeForeignFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
