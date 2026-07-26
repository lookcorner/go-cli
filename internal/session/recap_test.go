package session

import (
	"testing"
	"time"
)

func TestRecapActivityAtCountsCompletedLiveTurns(t *testing.T) {
	logger, err := NewLoggerWithID(t.TempDir(), "recap-activity")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	for index := 0; index < 2; index++ {
		if err := logger.Append("user_prompt", map[string]any{"text": "prompt"}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("model_response", map[string]any{
			"response_id": "response", "text": "done", "tool_call_count": 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "tool-step", "tool_call_count": 1,
	}); err != nil {
		t.Fatal(err)
	}

	activity, err := RecapActivityAt(logger.Path())
	if err != nil || activity.CompletedTurns != 2 || activity.LastCompleted.IsZero() ||
		time.Since(activity.LastCompleted) > time.Minute {
		t.Fatalf("activity=%+v err=%v", activity, err)
	}
}
