package session

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"time"
)

type SummaryIdentity struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type Summary struct {
	Info            SummaryIdentity `json:"info"`
	SessionSummary  string          `json:"session_summary"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	NumMessages     int             `json:"num_messages"`
	NumChatMessages int             `json:"num_chat_messages"`
	CurrentModelID  string          `json:"current_model_id"`
	ParentSessionID *string         `json:"parent_session_id,omitempty"`
	HeadCommit      *string         `json:"head_commit,omitempty"`
	LastActiveAt    *time.Time      `json:"last_active_at,omitempty"`
	GeneratedTitle  *string         `json:"generated_title,omitempty"`
	TitleIsManual   bool            `json:"title_is_manual,omitempty"`
}

func Summaries(dir, cwd string, limit int) ([]Summary, error) {
	items, err := List(dir, cwd)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	summaries := make([]Summary, 0, len(items))
	for _, item := range items {
		summary, err := summaryFromInfo(dir, item)
		if err == nil {
			summaries = append(summaries, summary)
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return summaries[i].Info.ID < summaries[j].Info.ID
		}
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries, nil
}

func summaryFromInfo(dir string, info Info) (Summary, error) {
	path, err := PathForID(dir, info.SessionID)
	if err != nil {
		return Summary{}, err
	}
	if err := validateSessionFile(path); err != nil {
		return Summary{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer file.Close()
	result := Summary{
		Info: SummaryIdentity{ID: info.SessionID, CWD: info.CWD}, SessionSummary: info.Title,
		UpdatedAt: info.UpdatedAt, CurrentModelID: info.ModelID,
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event storedEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		result.NumMessages++
		if result.CreatedAt.IsZero() && !event.Time.IsZero() {
			result.CreatedAt = event.Time
		}
		if event.Time.After(result.UpdatedAt) {
			result.UpdatedAt = event.Time
		}
		switch event.Kind {
		case "user_prompt", "model_response":
			result.NumChatMessages++
			if result.LastActiveAt == nil || event.Time.After(*result.LastActiveAt) {
				value := event.Time
				result.LastActiveAt = &value
			}
		case "session_forked":
			var data struct {
				ParentSessionID string `json:"parent_session_id"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.ParentSessionID != "" {
				result.ParentSessionID = &data.ParentSessionID
			}
		case "session_title":
			var data struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.Title != "" {
				result.GeneratedTitle = &data.Title
				result.TitleIsManual = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, err
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = result.UpdatedAt
	}
	if info.HeadCommit != "" {
		result.HeadCommit = &info.HeadCommit
	}
	return result, nil
}
