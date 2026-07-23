package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

type suggestionStreamer struct {
	mu       sync.Mutex
	result   api.StreamResult
	err      error
	clones   []bool
	requests []api.ResponseRequest
}

func (s *suggestionStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	return (suggestionClone{s}).StreamResponse(ctx, request, onText)
}

func (s *suggestionStreamer) CloneForCompaction(includeHistory bool) api.Streamer {
	s.mu.Lock()
	s.clones = append(s.clones, includeHistory)
	s.mu.Unlock()
	return suggestionClone{s}
}

type suggestionClone struct{ parent *suggestionStreamer }

func (s suggestionClone) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.parent.requests = append(s.parent.requests, request)
	return s.parent.result, s.parent.err
}

func suggestionSessionPath(t *testing.T, user, assistant string) string {
	t.Helper()
	logger, err := session.NewLoggerWithID(t.TempDir(), "suggest")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt(user, nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": assistant, "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	return logger.Path()
}

func TestSuggestPromptUsesBoundedIsolatedTranscript(t *testing.T) {
	streamer := &suggestionStreamer{result: api.StreamResult{Text: "\"run the tests\""}}
	runner := &Runner{Client: streamer, SessionPath: suggestionSessionPath(t, "fix the parser", "The parser is fixed.")}
	suggestion, err := runner.SuggestPrompt(context.Background(), "/work/project", "fast-model")
	if err != nil || suggestion != "run the tests" {
		t.Fatalf("suggestion=%q err=%v", suggestion, err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.clones) != 1 || streamer.clones[0] || len(streamer.requests) != 1 {
		t.Fatalf("clones=%v requests=%#v", streamer.clones, streamer.requests)
	}
	request := streamer.requests[0]
	content, _ := request.Input[0].Content.(string)
	if request.Model != "fast-model" || request.PreviousResponseID != "" || len(request.Tools) != 0 || !strings.Contains(content, "CWD: /work/project") || !strings.Contains(content, "User: fix the parser") || !strings.Contains(content, "Agent: The parser is fixed.") {
		t.Fatalf("request=%#v", request)
	}
}

func TestSuggestShellUsesIsolatedBoundedRequest(t *testing.T) {
	streamer := &suggestionStreamer{result: api.StreamResult{Text: "git commit --amend"}}
	runner := &Runner{Client: streamer}
	got, err := runner.SuggestShell(context.Background(), "git", "/work/project", "fast-model")
	if err != nil || got != "git commit --amend" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.clones) != 1 || streamer.clones[0] || len(streamer.requests) != 1 {
		t.Fatalf("clones=%v requests=%#v", streamer.clones, streamer.requests)
	}
	request := streamer.requests[0]
	content, _ := request.Input[0].Content.(string)
	if request.Model != "fast-model" || request.PreviousResponseID != "" || len(request.Tools) != 0 || request.MaxOutputTokens != 50 || request.Temperature == nil || *request.Temperature != 0.1 || !strings.Contains(content, "CWD: /work/project") || !strings.Contains(content, "Partial command: git") {
		t.Fatalf("request=%#v", request)
	}
}

func TestSuggestShellRejectsUnavailableInputs(t *testing.T) {
	if _, err := (&Runner{}).SuggestShell(context.Background(), "git", "/work", ""); err == nil {
		t.Fatal("missing client was accepted")
	}
	streamer := &suggestionStreamer{result: api.StreamResult{}}
	if _, err := (&Runner{Client: streamer}).SuggestShell(context.Background(), "", "/work", ""); err == nil {
		t.Fatal("empty prefix was accepted")
	}
}

func TestSuggestPromptModelPrecedenceAndFailures(t *testing.T) {
	path := suggestionSessionPath(t, "fix it", "Fixed.")
	t.Run("environment wins", func(t *testing.T) {
		t.Setenv("GROK_PROMPT_SUGGESTIONS_MODEL", "env-model")
		streamer := &suggestionStreamer{result: api.StreamResult{Text: "commit this"}}
		if _, err := (&Runner{Client: streamer, SessionPath: path}).SuggestPrompt(context.Background(), "/work", "hint-model"); err != nil {
			t.Fatal(err)
		}
		if streamer.requests[0].Model != "env-model" {
			t.Fatalf("model=%q", streamer.requests[0].Model)
		}
	})
	t.Run("default model", func(t *testing.T) {
		streamer := &suggestionStreamer{result: api.StreamResult{Text: "continue"}}
		if _, err := (&Runner{Client: streamer, SessionPath: path}).SuggestPrompt(context.Background(), "/work", ""); err != nil {
			t.Fatal(err)
		}
		if streamer.requests[0].Model != defaultPromptSuggestionModel {
			t.Fatalf("model=%q", streamer.requests[0].Model)
		}
	})
	t.Run("sampling failure", func(t *testing.T) {
		streamer := &suggestionStreamer{err: errors.New("offline")}
		if _, err := (&Runner{Client: streamer, SessionPath: path}).SuggestPrompt(context.Background(), "/work", ""); err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestSanitizePromptSuggestionRejectsTerminalControlCharacters(t *testing.T) {
	for _, suggestion := range []string{"run tests\x1b[2J", "run\ttests", "approve\a now"} {
		if got := sanitizePromptSuggestion(suggestion); got != "" {
			t.Fatalf("sanitizePromptSuggestion(%q)=%q", suggestion, got)
		}
	}
}

func TestPromptSuggestionFilteringAndRepeatGuard(t *testing.T) {
	accepted := map[string]string{
		"run the tests":          "run the tests",
		"`push it`":              "push it",
		"yes":                    "yes",
		"/review":                "/review",
		"run tests\nthen commit": "run tests",
	}
	for input, want := range accepted {
		if got := sanitizePromptSuggestion(input); got != want {
			t.Errorf("sanitize(%q)=%q want %q", input, got, want)
		}
	}
	for _, input := range []string{"NONE", "refactor", "I'll run tests", "Suggestion: run tests", "**run tests**", "Run tests. Then commit.", strings.Repeat("word ", 20)} {
		if got := sanitizePromptSuggestion(input); got != "" {
			t.Errorf("sanitize(%q)=%q", input, got)
		}
	}
	messages := []session.Message{{Role: "user", Text: "Fix  the flaky\nauth test."}, {Role: "assistant", Text: "Fixed."}}
	if !promptSuggestionRepeatsUser("fix the flaky auth test!", messages) || promptSuggestionRepeatsUser("run the tests", messages) {
		t.Fatal("repeat guard did not preserve the reference word threshold")
	}
}

func TestPromptSuggestionTranscriptRequiresAssistantAndKeepsNewest(t *testing.T) {
	if got := promptSuggestionTranscript([]session.Message{{Role: "user", Text: "hello"}}); got != "" {
		t.Fatalf("transcript=%q", got)
	}
	long := strings.Repeat("界", suggestionMessageBytes)
	messages := make([]session.Message, 0, 42)
	for range 20 {
		messages = append(messages, session.Message{Role: "user", Text: long}, session.Message{Role: "assistant", Text: long})
	}
	messages = append(messages, session.Message{Role: "user", Text: "newest question"}, session.Message{Role: "assistant", Text: "newest answer"})
	got := promptSuggestionTranscript(messages)
	if !utf8.ValidString(got) || !strings.Contains(got, "newest question") || !strings.HasSuffix(got, "Agent: newest answer") || len(got) > suggestionTranscriptBytes+suggestionMessageBytes+64 {
		t.Fatalf("invalid bounded transcript bytes=%d", len(got))
	}
}
