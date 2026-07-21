package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type SearchRequest struct {
	Query          string
	CWD            string
	Limit          int
	Offset         int
	IncludeContent bool
}

type SearchHit struct {
	SessionID     string   `json:"sessionId"`
	CWD           string   `json:"cwd"`
	Summary       string   `json:"summary"`
	UpdatedAt     string   `json:"updatedAt"`
	Score         float64  `json:"score"`
	MatchedFields []string `json:"matchedFields"`
	Snippet       *string  `json:"snippet,omitempty"`
}

type SearchResult struct {
	Results       []SearchHit `json:"results"`
	NextOffset    *int        `json:"nextOffset"`
	TotalEstimate *int        `json:"totalEstimate"`
	Bootstrapping bool        `json:"bootstrapping"`
}

type promptRecord struct {
	text      string
	time      time.Time
	sessionID string
	index     int
}

const MaxPromptHistoryEntries = 10_000

func PromptHistory(dir, cwd, sessionID string, newestFirst bool) ([]string, error) {
	if strings.TrimSpace(cwd) == "" {
		return nil, errors.New("cwd is required")
	}
	items, err := List(dir, cwd)
	if err != nil {
		return nil, err
	}
	var records []promptRecord
	for _, item := range items {
		if sessionID != "" && item.SessionID != sessionID {
			continue
		}
		path, err := PathForID(dir, item.SessionID)
		if err != nil {
			continue
		}
		prompts, err := promptRecords(path, item.SessionID)
		if err == nil {
			records = append(records, prompts...)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].time.Equal(records[j].time) {
			if records[i].sessionID == records[j].sessionID {
				if newestFirst {
					return records[i].index > records[j].index
				}
				return records[i].index < records[j].index
			}
			return records[i].sessionID < records[j].sessionID
		}
		if newestFirst {
			return records[i].time.After(records[j].time)
		}
		return records[i].time.Before(records[j].time)
	})
	prompts := make([]string, 0, len(records))
	for _, record := range records {
		if record.text != "" && (len(prompts) == 0 || prompts[len(prompts)-1] != record.text) {
			prompts = append(prompts, record.text)
			if len(prompts) == MaxPromptHistoryEntries {
				break
			}
		}
	}
	return prompts, nil
}

func promptRecords(path, sessionID string) ([]promptRecord, error) {
	if err := validateSessionFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []promptRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event storedEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Kind != "user_prompt" {
			continue
		}
		var data struct {
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
		}
		if json.Unmarshal(event.Data, &data) == nil && !data.Synthetic {
			records = append(records, promptRecord{text: data.Text, time: event.Time, sessionID: sessionID, index: len(records)})
		}
	}
	return records, scanner.Err()
}

func InfoByID(dir, id string) (Info, error) {
	path, err := PathForID(dir, id)
	if err != nil {
		return Info{}, err
	}
	return readInfo(path, id)
}

func Rename(dir, id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title must not be blank")
	}
	path, err := PathForID(dir, id)
	if err != nil {
		return err
	}
	return appendAdminEvent(path, Event{Time: time.Now().UTC(), Kind: "session_title", Data: map[string]any{"title": title}})
}

func Delete(dir, id string) error {
	path, err := PathForID(dir, id)
	if err != nil {
		return err
	}
	if err := validateSessionFile(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete session log: %w", err)
	}
	artifactDir, err := ArtifactDir(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(artifactDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(artifactDir)
	}
	return os.RemoveAll(artifactDir)
}

func Search(dir string, req SearchRequest) (SearchResult, error) {
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" {
		return SearchResult{}, errors.New("query must not be blank")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	if req.Offset < 0 {
		return SearchResult{}, errors.New("offset must not be negative")
	}
	items, err := List(dir, req.CWD)
	if err != nil {
		return SearchResult{}, err
	}
	hits := make([]SearchHit, 0)
	for _, item := range items {
		content, err := searchableContent(dir, item.SessionID)
		if err != nil {
			continue
		}
		titleMatch := strings.Contains(strings.ToLower(item.Title), query)
		contentLower := strings.ToLower(content)
		contentMatch := strings.Contains(contentLower, query)
		if !titleMatch && !contentMatch {
			continue
		}
		fields := make([]string, 0, 2)
		score := float64(strings.Count(contentLower, query))
		if titleMatch {
			fields = append(fields, "title")
			score += 10
		}
		if contentMatch {
			fields = append(fields, "content")
		}
		var snippet *string
		if req.IncludeContent && contentMatch {
			value := searchSnippet(content, query)
			snippet = &value
		}
		hits = append(hits, SearchHit{
			SessionID: item.SessionID, CWD: item.CWD, Summary: item.Title,
			UpdatedAt: item.UpdatedAt.Format(time.RFC3339), Score: score,
			MatchedFields: fields, Snippet: snippet,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].UpdatedAt > hits[j].UpdatedAt
		}
		return hits[i].Score > hits[j].Score
	})
	total := len(hits)
	start := req.Offset
	if start > total {
		start = total
	}
	end := start + req.Limit
	if end > total {
		end = total
	}
	var next *int
	if end < total {
		value := end
		next = &value
	}
	return SearchResult{Results: hits[start:end], NextOffset: next, TotalEstimate: &total}, nil
}

func appendAdminEvent(path string, event Event) error {
	if err := validateSessionFile(path); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	linked, linkErr := os.Lstat(path)
	if statErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		file.Close()
		return errors.New("session log changed before update")
	}
	needsNewline, err := sessionNeedsNewline(path)
	if err != nil {
		file.Close()
		return err
	}
	if needsNewline {
		encoded = append([]byte{'\n'}, encoded...)
	}
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func searchableContent(dir, id string) (string, error) {
	path, err := PathForID(dir, id)
	if err != nil {
		return "", err
	}
	if err := validateSessionFile(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var content strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event storedEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Kind != "user_prompt" && event.Kind != "model_response" {
			continue
		}
		var data struct {
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
		}
		if json.Unmarshal(event.Data, &data) == nil && data.Text != "" && !(event.Kind == "user_prompt" && data.Synthetic) {
			content.WriteString(data.Text)
			content.WriteByte('\n')
		}
	}
	return content.String(), scanner.Err()
}

func searchSnippet(content, lowerQuery string) string {
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(strings.ToLower(line), lowerQuery) {
			continue
		}
		runes := []rune(strings.TrimSpace(line))
		if len(runes) > 160 {
			runes = append(runes[:157], '.', '.', '.')
		}
		return string(runes)
	}
	return ""
}
