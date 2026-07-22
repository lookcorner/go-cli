package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

const (
	defaultPromptSuggestionModel = "grok-build-0.1"
	suggestionTranscriptBytes    = 24_000
	suggestionMessageBytes       = 1_500
	suggestionMaxBytes           = 120
	suggestionMaxWords           = 16
)

const promptSuggestionInstructions = `You predict what the USER will type next into their coding agent CLI.
Use the transcript to predict what they were just about to type, not what they should do.
Be specific, stay in the user's voice, and do not repeat a completed request.
Return one short message of 2-12 words, or NONE when the next step is not obvious.
Return only the suggestion: no quotes, markdown, labels, questions, or explanation.`

var errPromptSuggestionUnavailable = errors.New("prompt suggestion unavailable")

// SuggestPrompt predicts the next user message from a bounded, text-only
// transcript without changing the parent model history.
func (r *Runner) SuggestPrompt(ctx context.Context, cwd, modelOverride string) (string, error) {
	if r == nil || r.Client == nil || strings.TrimSpace(r.SessionPath) == "" {
		return "", errPromptSuggestionUnavailable
	}
	messages, err := session.Transcript(r.SessionPath)
	if err != nil {
		return "", errPromptSuggestionUnavailable
	}
	transcript := promptSuggestionTranscript(messages)
	if transcript == "" {
		return "", errPromptSuggestionUnavailable
	}
	model := strings.TrimSpace(os.Getenv("GROK_PROMPT_SUGGESTIONS_MODEL"))
	if model == "" {
		model = strings.TrimSpace(modelOverride)
	}
	if model == "" {
		model = defaultPromptSuggestionModel
	}
	streamer := r.Client
	if cloner, ok := r.Client.(api.CompactionCloner); ok {
		streamer = cloner.CloneForCompaction(false)
	}
	response, err := streamer.StreamResponse(ctx, api.ResponseRequest{
		Model: model, Instructions: promptSuggestionInstructions,
		Input:  []api.InputItem{{Type: "message", Role: "user", Content: "CWD: " + cwd + "\n\nTranscript:\n\n" + transcript + "\n\nPredict the user's next message."}},
		Stream: true,
	}, nil)
	if err != nil {
		return "", err
	}
	suggestion := sanitizePromptSuggestion(response.Text)
	if suggestion == "" || promptSuggestionRepeatsUser(suggestion, messages) {
		return "", errPromptSuggestionUnavailable
	}
	return suggestion, nil
}

func promptSuggestionTranscript(messages []session.Message) string {
	lines := make([]string, 0, len(messages))
	used := 0
	sawAssistant := false
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(message.Text)
		if len(message.Content) > 0 {
			parts := make([]string, 0, len(message.Content))
			for _, part := range message.Content {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					parts = append(parts, strings.TrimSpace(part.Text))
				}
			}
			if len(parts) > 0 {
				text = strings.Join(parts, "\n")
			}
		}
		if text == "" {
			continue
		}
		text = truncateSuggestionText(text, suggestionMessageBytes)
		role := "User"
		if message.Role == "assistant" {
			role = "Agent"
			sawAssistant = true
		}
		line := role + ": " + text
		if used+len(line) > suggestionTranscriptBytes && len(lines) > 0 {
			break
		}
		used += len(line)
		lines = append(lines, line)
	}
	if !sawAssistant {
		return ""
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n\n")
}

func truncateSuggestionText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func sanitizePromptSuggestion(raw string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(raw), "\n")
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimRight(strings.TrimSpace(line), "\"'`\u201d\u2019"), "\"'`\u201c\u2018"))
	if line == "" || len(line) >= suggestionMaxBytes {
		return ""
	}
	lower := strings.ToLower(line)
	for _, meta := range []string{"none", "n/a", "no suggestion", "nothing", "(silence)", "silence", "null"} {
		if lower == meta || strings.HasPrefix(lower, meta+".") {
			return ""
		}
	}
	if strings.Contains(line, "*") || strings.Contains(line, "```") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
		return ""
	}
	for _, prefix := range []string{"i'll ", "i will ", "let me ", "here's ", "here is ", "i'm going to "} {
		if strings.HasPrefix(lower, prefix) {
			return ""
		}
	}
	if strings.HasPrefix(line, "(") && strings.HasSuffix(line, ")") || strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
		return ""
	}
	if head, _, ok := strings.Cut(line, ":"); ok && !strings.Contains(head, " ") {
		alphabetic := head != ""
		for _, char := range head {
			alphabetic = alphabetic && (char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z')
		}
		if alphabetic {
			return ""
		}
	}
	for index := 0; index+2 < len(line); index++ {
		if strings.ContainsRune(".!?", rune(line[index])) && line[index+1] == ' ' && line[index+2] >= 'A' && line[index+2] <= 'Z' {
			return ""
		}
	}
	words := strings.Fields(line)
	if len(words) > suggestionMaxWords {
		return ""
	}
	if len(words) == 1 {
		bare := strings.TrimRight(lower, ".!")
		allowed := []string{"yes", "yeah", "yep", "no", "ok", "okay", "continue", "proceed", "push", "commit", "deploy", "stop", "check", "retry", "undo", "merge"}
		if !slices.Contains(allowed, bare) && !strings.HasPrefix(bare, "/") {
			return ""
		}
	}
	return line
}

func promptSuggestionRepeatsUser(suggestion string, messages []session.Message) bool {
	if len(strings.Fields(suggestion)) < 4 {
		return false
	}
	normalized := normalizePromptSuggestion(suggestion)
	for _, message := range messages {
		if message.Role == "user" && normalizePromptSuggestion(message.Text) == normalized {
			return true
		}
	}
	return false
}

func normalizePromptSuggestion(value string) string {
	return strings.ToLower(strings.TrimRight(strings.Join(strings.Fields(value), " "), ".!?"))
}
