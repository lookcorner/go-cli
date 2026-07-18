package session

import (
	"testing"
)

func TestSummariesFoldSessionEvents(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	logger, err := NewLoggerWithID(dir, "summary-session")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "model-one", "headCommit": "abc123"})
	_ = logger.Append("user_prompt", map[string]any{"text": "Initial title"})
	_ = logger.Append("model_response", map[string]any{"text": "answer", "response_id": "r1", "tool_call_count": 0})
	_ = logger.Append("session_forked", map[string]any{"parent_session_id": "parent-session"})
	_ = logger.Append("session_title", map[string]any{"title": "Manual title"})
	_ = logger.Close()

	summaries, err := Summaries(dir, cwd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries=%#v", summaries)
	}
	summary := summaries[0]
	if summary.Info.ID != "summary-session" || summary.Info.CWD != cwd || summary.SessionSummary != "Manual title" {
		t.Fatalf("identity/title=%#v", summary)
	}
	if summary.NumMessages != 5 || summary.NumChatMessages != 2 || summary.CurrentModelID != "model-one" {
		t.Fatalf("counts/model=%#v", summary)
	}
	if summary.ParentSessionID == nil || *summary.ParentSessionID != "parent-session" || summary.HeadCommit == nil || *summary.HeadCommit != "abc123" {
		t.Fatalf("fork/git metadata=%#v", summary)
	}
	if summary.GeneratedTitle == nil || *summary.GeneratedTitle != "Manual title" || !summary.TitleIsManual || summary.CreatedAt.IsZero() || summary.LastActiveAt == nil {
		t.Fatalf("summary metadata=%#v", summary)
	}
}
