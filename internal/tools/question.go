package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
)

const userQuestionCancelText = "User declined to answer the questions. Continue with the task using your best judgment, or ask different questions."

type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview,omitempty"`
}

type UserQuestion struct {
	Question    string               `json:"question"`
	Options     []UserQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multi_select,omitempty"`
}

func (q *UserQuestion) UnmarshalJSON(data []byte) error {
	var wire struct {
		Question          string               `json:"question"`
		Options           []UserQuestionOption `json:"options"`
		MultiSelect       *bool                `json:"multi_select"`
		LegacyMultiSelect *bool                `json:"multiSelect"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	q.Question, q.Options = wire.Question, wire.Options
	if wire.MultiSelect != nil {
		q.MultiSelect = *wire.MultiSelect
	} else if wire.LegacyMultiSelect != nil {
		q.MultiSelect = *wire.LegacyMultiSelect
	}
	return nil
}

type UserQuestionAnnotation struct {
	Preview string `json:"preview,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

func ParseUserQuestionAnswer(question UserQuestion, value string) ([]string, UserQuestionAnnotation, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, UserQuestionAnnotation{}, errors.New("answer is required")
	}
	parts := []string{value}
	if question.MultiSelect {
		parts = strings.Split(value, ",")
	}
	answers := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	annotation := UserQuestionAnnotation{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, preview := "", ""
		if index, err := strconv.Atoi(part); err == nil {
			if index < 1 || index > len(question.Options) {
				return nil, UserQuestionAnnotation{}, fmt.Errorf("option %d is out of range", index)
			}
			label, preview = question.Options[index-1].Label, question.Options[index-1].Preview
		} else {
			for _, option := range question.Options {
				if strings.EqualFold(part, option.Label) {
					label, preview = option.Label, option.Preview
					break
				}
			}
			if label == "" {
				label = "Other"
				if annotation.Notes == "" {
					annotation.Notes = part
				} else {
					annotation.Notes += ", " + part
				}
			}
		}
		if !seen[label] {
			answers = append(answers, label)
			seen[label] = true
		}
		if !question.MultiSelect && preview != "" {
			annotation.Preview = preview
		}
	}
	if len(answers) == 0 {
		return nil, UserQuestionAnnotation{}, errors.New("answer is required")
	}
	return answers, annotation, nil
}

type UserQuestionRequest struct {
	ToolCallID string
	Questions  []UserQuestion
	Mode       string
}

type UserQuestionResponse struct {
	Outcome        string                            `json:"outcome"`
	Answers        map[string][]string               `json:"answers,omitempty"`
	Annotations    map[string]UserQuestionAnnotation `json:"annotations,omitempty"`
	PartialAnswers map[string]string                 `json:"partial_answers,omitempty"`
}

type UserQuestionObserver interface {
	AskUserQuestion(context.Context, UserQuestionRequest) (UserQuestionResponse, error)
}

type UserQuestions struct {
	mu       sync.RWMutex
	observer UserQuestionObserver
	plan     *PlanMode
}

func (q *UserQuestions) SetObserver(observer UserQuestionObserver) {
	q.mu.Lock()
	q.observer = observer
	q.mu.Unlock()
}

func (q *UserQuestions) ask(ctx context.Context, request UserQuestionRequest) (UserQuestionResponse, error) {
	q.mu.RLock()
	observer := q.observer
	q.mu.RUnlock()
	if observer == nil {
		return UserQuestionResponse{Outcome: "questions_sent"}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, userQuestionTimeout())
	defer cancel()
	response, err := observer.AskUserQuestion(waitCtx, request)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return UserQuestionResponse{Outcome: "cancelled"}, nil
	}
	return response, err
}

func userQuestionTimeout() time.Duration {
	if seconds, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("GROK_ASK_USER_QUESTION_TIMEOUT_SECS")), 10, 64); err == nil && seconds > 0 && seconds <= uint64((1<<63-1)/int64(time.Second)) {
		return time.Duration(seconds) * time.Second
	}
	return 30 * time.Minute
}

type askUserQuestionTool struct{ questions *UserQuestions }

func (t *askUserQuestionTool) Definition() api.ToolDefinition {
	option := objectSchema(map[string]any{
		"label":       map[string]any{"type": "string", "description": "Option text shown to the user. A few words at most."},
		"description": map[string]any{"type": "string", "description": "What picking this option means or implies."},
		"preview":     map[string]any{"type": "string", "description": "Optional content shown while this single-select option is focused."},
	}, "label", "description")
	question := objectSchema(map[string]any{
		"question":     map[string]any{"type": "string", "description": "The question to ask, phrased as a full question."},
		"options":      map[string]any{"type": "array", "items": option},
		"multi_select": map[string]any{"type": "boolean", "description": "Let the user pick more than one option."},
	}, "question", "options")
	return api.ToolDefinition{
		Type: "function", Name: "ask_user_question",
		Description: "Ask the user one or more multiple-choice questions. Every question automatically gets an Other choice. Put the recommended option first and append (Recommended) to its label.",
		Parameters:  objectSchema(map[string]any{"questions": map[string]any{"type": "array", "items": question}}, "questions"),
	}
}

func (t *askUserQuestionTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Questions []UserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("decode ask_user_question arguments: %w", err)
	}
	if len(input.Questions) == 0 {
		return "No questions provided. Continue with the task.", nil
	}
	seen := make(map[string]bool, len(input.Questions))
	for _, question := range input.Questions {
		if seen[question.Question] {
			return "", fmt.Errorf("duplicate question text: %q", question.Question)
		}
		seen[question.Question] = true
	}
	call, _ := ToolCallFromContext(ctx)
	mode := "default"
	if t.questions.plan != nil && t.questions.plan.Active() {
		mode = "plan"
	}
	response, err := t.questions.ask(ctx, UserQuestionRequest{ToolCallID: call.ID, Questions: input.Questions, Mode: mode})
	if err != nil {
		return "", err
	}
	switch response.Outcome {
	case "questions_sent":
		lines := make([]string, 0, len(input.Questions))
		for index, question := range input.Questions {
			labels := make([]string, 0, len(question.Options))
			for _, option := range question.Options {
				labels = append(labels, option.Label)
			}
			lines = append(lines, fmt.Sprintf("%d. %s [options: %s]", index+1, question.Question, strings.Join(labels, ", ")))
		}
		return "Your questions have been presented to the user for answering:\n" + strings.Join(lines, "\n"), nil
	case "accepted":
		return formatAcceptedAnswers(input.Questions, response), nil
	case "chat_about_this":
		return formatPartialAnswers(input.Questions, response.PartialAnswers, true), nil
	case "skip_interview":
		return formatPartialAnswers(input.Questions, response.PartialAnswers, false), nil
	case "cancelled":
		return userQuestionCancelText, nil
	default:
		return "", errors.New("invalid user question outcome")
	}
}

func formatAcceptedAnswers(questions []UserQuestion, response UserQuestionResponse) string {
	entries := make([]string, 0, len(response.Answers))
	for _, question := range questions {
		answers, ok := response.Answers[question.Question]
		if !ok {
			continue
		}
		entry := "\"" + question.Question + "\"=\"" + strings.Join(answers, ", ") + "\""
		if annotation, ok := response.Annotations[question.Question]; ok {
			if annotation.Preview != "" {
				entry += " selected preview:\n" + annotation.Preview
			}
			if annotation.Notes != "" {
				entry += " user notes: " + annotation.Notes
			}
		}
		entries = append(entries, entry)
	}
	return "User has answered your questions: " + strings.Join(entries, ", ") + ". You can now continue with the user's answers in mind."
}

func formatPartialAnswers(questions []UserQuestion, answers map[string]string, chat bool) string {
	lines := make([]string, 0, len(questions))
	for _, question := range questions {
		line := "- \"" + question.Question + "\"\n  "
		if answer, ok := answers[question.Question]; ok {
			line += "Answer: " + answer
		} else {
			line += "(No answer provided)"
		}
		lines = append(lines, line)
	}
	if chat {
		return "The user wants to clarify these questions.\n    This means they may have additional information, context or questions for you.\n    Take their response into account and then reformulate the questions if appropriate.\n    Start by asking them what they would like to clarify.\n\n    Questions asked:\n" + strings.Join(lines, "\n")
	}
	return "The user has indicated they have provided enough answers for the plan interview.\nStop asking clarifying questions and proceed to finish the plan with the information you have.\n\nQuestions asked and answers provided:\n" + strings.Join(lines, "\n")
}
