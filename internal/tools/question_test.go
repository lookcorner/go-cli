package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type recordingQuestionObserver struct {
	request  UserQuestionRequest
	response UserQuestionResponse
}

type deadlineQuestionObserver struct{ hasDeadline bool }

func (o *deadlineQuestionObserver) AskUserQuestion(ctx context.Context, _ UserQuestionRequest) (UserQuestionResponse, error) {
	_, o.hasDeadline = ctx.Deadline()
	return UserQuestionResponse{Outcome: "cancelled"}, nil
}

type blockingQuestionObserver struct{}

func (blockingQuestionObserver) AskUserQuestion(ctx context.Context, _ UserQuestionRequest) (UserQuestionResponse, error) {
	<-ctx.Done()
	return UserQuestionResponse{}, ctx.Err()
}

func (o *recordingQuestionObserver) AskUserQuestion(_ context.Context, request UserQuestionRequest) (UserQuestionResponse, error) {
	o.request = request
	return o.response, nil
}

func TestAskUserQuestionAcceptedInPlanMode(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	observer := &recordingQuestionObserver{response: UserQuestionResponse{
		Outcome:     "accepted",
		Answers:     map[string][]string{"Which database?": {"SQLite"}},
		Annotations: map[string]UserQuestionAnnotation{"Which database?": {Preview: "schema.sql", Notes: "keep it local"}},
	}}
	registry.SetUserQuestionObserver(observer)
	if err := registry.SetPlanMode(true); err != nil {
		t.Fatal(err)
	}
	output, err := registry.Execute(WithToolCall(context.Background(), "ask-1", "ask_user_question"), "ask_user_question", json.RawMessage(`{
		"questions":[{"question":"Which database?","options":[{"label":"SQLite","description":"Local file"}],"multi_select":true}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `User has answered your questions: "Which database?"="SQLite" selected preview:
schema.sql user notes: keep it local. You can now continue with the user's answers in mind.`
	if output != want {
		t.Fatalf("output=%q", output)
	}
	if observer.request.ToolCallID != "ask-1" || observer.request.Mode != "plan" || !observer.request.Questions[0].MultiSelect {
		t.Fatalf("request=%#v", observer.request)
	}
}

func TestAskUserQuestionResultPathsAndFallback(t *testing.T) {
	question := UserQuestion{Question: "Choose?", Options: []UserQuestionOption{{Label: "A"}, {Label: "B"}}}
	tests := []struct {
		name     string
		response UserQuestionResponse
		contains string
	}{
		{name: "chat", response: UserQuestionResponse{Outcome: "chat_about_this", PartialAnswers: map[string]string{"Choose?": "A"}}, contains: "The user wants to clarify these questions."},
		{name: "skip", response: UserQuestionResponse{Outcome: "skip_interview"}, contains: "Stop asking clarifying questions"},
		{name: "cancel", response: UserQuestionResponse{Outcome: "cancelled"}, contains: userQuestionCancelText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := &UserQuestions{}
			coordinator.SetObserver(&recordingQuestionObserver{response: tt.response})
			tool := &askUserQuestionTool{questions: coordinator}
			raw, _ := json.Marshal(map[string]any{"questions": []UserQuestion{question}})
			output, err := tool.Execute(context.Background(), raw)
			if err != nil || !strings.Contains(output, tt.contains) {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
	tool := &askUserQuestionTool{questions: &UserQuestions{}}
	raw, _ := json.Marshal(map[string]any{"questions": []UserQuestion{question}})
	output, err := tool.Execute(context.Background(), raw)
	if err != nil || output != "Your questions have been presented to the user for answering:\n1. Choose? [options: A, B]" {
		t.Fatalf("fallback=%q err=%v", output, err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"questions":[{"question":"same"},{"question":"same"}]}`))
	if err == nil || !strings.Contains(err.Error(), "duplicate question text") {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestUserQuestionLegacyMultiSelectAndTimeoutConfiguration(t *testing.T) {
	var question UserQuestion
	if err := json.Unmarshal([]byte(`{"question":"Pick?","multiSelect":true}`), &question); err != nil || !question.MultiSelect {
		t.Fatalf("question=%#v err=%v", question, err)
	}
	observer := &deadlineQuestionObserver{}
	questions := &UserQuestions{observer: observer}
	questions.Configure(false, 7*time.Second)
	if _, err := questions.ask(context.Background(), UserQuestionRequest{}); err != nil || observer.hasDeadline {
		t.Fatalf("disabled timeout deadline=%v err=%v", observer.hasDeadline, err)
	}
	questions.Configure(true, 7*time.Second)
	if _, err := questions.ask(context.Background(), UserQuestionRequest{}); err != nil || !observer.hasDeadline {
		t.Fatalf("enabled timeout deadline=%v err=%v", observer.hasDeadline, err)
	}
	questions.SetObserver(blockingQuestionObserver{})
	questions.Configure(true, time.Millisecond)
	response, err := questions.ask(context.Background(), UserQuestionRequest{})
	if err != nil || response.Outcome != "cancelled" {
		t.Fatalf("timeout response=%#v err=%v", response, err)
	}
}

func TestParseUserQuestionAnswer(t *testing.T) {
	question := UserQuestion{Question: "Pick?", Options: []UserQuestionOption{{Label: "Fast", Preview: "fast preview"}, {Label: "Safe"}}}
	answers, annotation, err := ParseUserQuestionAnswer(question, "1")
	if err != nil || strings.Join(answers, ",") != "Fast" || annotation.Preview != "fast preview" {
		t.Fatalf("answers=%#v annotation=%#v err=%v", answers, annotation, err)
	}
	question.MultiSelect = true
	answers, annotation, err = ParseUserQuestionAnswer(question, "2, custom details")
	if err != nil || strings.Join(answers, ",") != "Safe,Other" || annotation.Notes != "custom details" {
		t.Fatalf("answers=%#v annotation=%#v err=%v", answers, annotation, err)
	}
	if _, _, err := ParseUserQuestionAnswer(question, "9"); err == nil {
		t.Fatal("out-of-range option was accepted")
	}
}
