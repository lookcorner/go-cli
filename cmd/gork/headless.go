package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

var sessionUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type headlessPrompt struct {
	text  string
	parts []api.ContentPart
}

type sessionStartup struct {
	resumePath string
	newID      string
	fork       bool
}

func normalizeOptionalResumeArgs(args []string) []string {
	normalized := append([]string(nil), args...)
	for index, arg := range normalized {
		if arg != "--resume" && arg != "-r" {
			continue
		}
		if index+1 == len(normalized) || normalized[index+1] == "--" || strings.HasPrefix(normalized[index+1], "-") {
			normalized[index] = arg + "="
		}
	}
	return normalized
}

func resolveSessionStartup(opts options, workspaceRoot string) (sessionStartup, error) {
	hasResume := opts.resumeSet || opts.continueLast
	if opts.continueLast && opts.resumeSet {
		return sessionStartup{}, errors.New("--continue cannot be combined with --resume or --load")
	}
	if opts.sessionID != "" && !sessionUUIDPattern.MatchString(opts.sessionID) {
		return sessionStartup{}, fmt.Errorf("--session-id must be a valid UUID (got %q)", cleanCLIText(opts.sessionID))
	}
	if opts.sessionID != "" && hasResume && !opts.forkSession {
		return sessionStartup{}, errors.New("--session-id requires --fork-session when resuming")
	}
	if opts.forkSession && !hasResume {
		return sessionStartup{}, errors.New("--fork-session requires --resume, --load, or --continue")
	}

	startup := sessionStartup{newID: opts.sessionID, fork: opts.forkSession}
	if !hasResume {
		return startup, nil
	}
	resume := opts.resume
	if opts.continueLast || resume == "" {
		items, err := session.List(opts.sessionDir, workspaceRoot)
		if err != nil {
			return sessionStartup{}, err
		}
		if len(items) == 0 {
			return sessionStartup{}, errors.New("no session found for current workspace")
		}
		resume = items[0].SessionID
	}
	switch {
	case resume == "latest":
		path, err := session.Latest(opts.sessionDir)
		if err != nil {
			return sessionStartup{}, err
		}
		startup.resumePath = path
	case filepath.IsAbs(resume) || strings.ContainsRune(resume, filepath.Separator) || filepath.Ext(resume) == ".jsonl":
		startup.resumePath = resume
	default:
		path, err := session.PathForID(opts.sessionDir, resume)
		if err != nil {
			return sessionStartup{}, err
		}
		startup.resumePath = path
	}
	if startup.fork {
		if startup.newID == "" {
			id, err := newSessionUUID()
			if err != nil {
				return sessionStartup{}, err
			}
			startup.newID = id
		}
	}
	return startup, nil
}

func newSessionUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

type headlessOutputFormat string

const (
	headlessOutputPlain         headlessOutputFormat = "plain"
	headlessOutputJSON          headlessOutputFormat = "json"
	headlessOutputStreamingJSON headlessOutputFormat = "streaming-json"
)

func loadHeadlessPrompt(opts options) (headlessPrompt, bool, error) {
	switch {
	case opts.singleSet:
		prompt, err := textHeadlessPrompt(opts.single)
		return prompt, true, flagError("--single", err)
	case opts.promptJSONSet:
		prompt, err := jsonHeadlessPrompt(opts.promptJSON)
		return prompt, true, flagError("--prompt-json", err)
	case opts.promptFileSet:
		data, err := os.ReadFile(opts.promptFile)
		if err != nil {
			return headlessPrompt{}, true, fmt.Errorf("read prompt file %q: %w", opts.promptFile, err)
		}
		var prompt headlessPrompt
		if strings.EqualFold(filepath.Ext(opts.promptFile), ".json") {
			prompt, err = jsonHeadlessPrompt(string(data))
		} else {
			prompt, err = textHeadlessPrompt(string(data))
		}
		return prompt, true, flagError("--prompt-file", err)
	default:
		return headlessPrompt{}, false, nil
	}
}

func flagError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}

func textHeadlessPrompt(value string) (headlessPrompt, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return headlessPrompt{}, errors.New("prompt is empty")
	}
	return headlessPrompt{text: value}, nil
}

func jsonHeadlessPrompt(value string) (headlessPrompt, error) {
	var raw any
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return headlessPrompt{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if object, ok := raw.(map[string]any); ok {
		if object["type"] != "acp" {
			return headlessPrompt{}, errors.New(`JSON object type must be "acp"`)
		}
		raw = object["content"]
	}
	blocks, ok := raw.([]any)
	if !ok {
		return headlessPrompt{}, errors.New(`expected an array or {"type":"acp","content":[...]} object`)
	}
	if len(blocks) == 0 {
		return headlessPrompt{}, errors.New("content blocks array is empty")
	}
	parts := make([]api.ContentPart, 0, len(blocks))
	var text []string
	for index, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return headlessPrompt{}, fmt.Errorf("content block %d must be an object", index+1)
		}
		kind, _ := block["type"].(string)
		switch kind {
		case "text":
			value, _ := block["text"].(string)
			if value == "" {
				return headlessPrompt{}, fmt.Errorf("content block %d text is required", index+1)
			}
			text = append(text, value)
			parts = append(parts, api.ContentPart{Type: "input_text", Text: value})
		case "image":
			imageURL, err := headlessImageURL(block)
			if err != nil {
				return headlessPrompt{}, fmt.Errorf("content block %d: %w", index+1, err)
			}
			parts = append(parts, api.ContentPart{Type: "input_image", ImageURL: imageURL})
		case "resource_link":
			name, _ := block["name"].(string)
			uri, _ := block["uri"].(string)
			if name == "" || uri == "" {
				return headlessPrompt{}, fmt.Errorf("content block %d resource links require name and uri", index+1)
			}
			value := fmt.Sprintf("Referenced resource %s: %s", name, uri)
			text = append(text, value)
			parts = append(parts, api.ContentPart{Type: "input_text", Text: value})
		case "resource":
			value, err := headlessResourceText(block)
			if err != nil {
				return headlessPrompt{}, fmt.Errorf("content block %d: %w", index+1, err)
			}
			text = append(text, value)
			parts = append(parts, api.ContentPart{Type: "input_text", Text: value})
		default:
			return headlessPrompt{}, fmt.Errorf("content block %d has unsupported type %q", index+1, kind)
		}
	}
	label := strings.TrimSpace(strings.Join(text, "\n\n"))
	if label == "" {
		label = "[image prompt]"
	}
	return headlessPrompt{text: label, parts: parts}, nil
}

func headlessResourceText(block map[string]any) (string, error) {
	resource, ok := block["resource"].(map[string]any)
	if !ok {
		return "", errors.New("embedded resources require a resource object")
	}
	uri, _ := resource["uri"].(string)
	text, _ := resource["text"].(string)
	blob, _ := resource["blob"].(string)
	mimeType, _ := resource["mimeType"].(string)
	if uri == "" {
		return "", errors.New("embedded resources require a uri")
	}
	if text == "" {
		if blob != "" {
			return "", errors.New("binary embedded resources are not supported")
		}
		return "", errors.New("embedded text resources require text")
	}
	header := "Embedded resource " + uri
	if mimeType != "" {
		header += " (" + mimeType + ")"
	}
	return header + ":\n" + text, nil
}

func headlessImageURL(block map[string]any) (string, error) {
	uri, _ := block["uri"].(string)
	if strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "http://") {
		return uri, nil
	}
	data, _ := block["data"].(string)
	mimeType, _ := block["mimeType"].(string)
	if mimeType == "" {
		mimeType, _ = block["mime_type"].(string)
	}
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("unsupported image mime type %q", mimeType)
	}
	if data == "" {
		return "", errors.New("image data is required")
	}
	return "data:" + mimeType + ";base64," + data, nil
}

func parseHeadlessOutputFormat(value string) (headlessOutputFormat, error) {
	switch headlessOutputFormat(strings.ToLower(strings.TrimSpace(value))) {
	case headlessOutputPlain:
		return headlessOutputPlain, nil
	case headlessOutputJSON:
		return headlessOutputJSON, nil
	case headlessOutputStreamingJSON:
		return headlessOutputStreamingJSON, nil
	default:
		return "", fmt.Errorf("invalid --output-format %q", cleanCLIText(value))
	}
}

func parseJSONSchema(value string) (map[string]any, error) {
	if value == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("--json-schema: invalid JSON: %w", err)
	}
	schema, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("--json-schema: must be a JSON object describing a JSON Schema")
	}
	return schema, nil
}

type headlessEmitter struct {
	format    headlessOutputFormat
	output    io.Writer
	sessionID string
	text      strings.Builder
	last      agent.Result
	usage     api.Usage
	turns     int
}

func (e *headlessEmitter) textWriter() io.Writer {
	if e.format == headlessOutputPlain {
		return e.output
	}
	if e.format == headlessOutputStreamingJSON {
		return e
	}
	return nil
}

func (e *headlessEmitter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if err := json.NewEncoder(e.output).Encode(map[string]any{"type": "text", "data": string(data)}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (e *headlessEmitter) add(result agent.Result) error {
	e.last = result
	e.turns++
	if result.Usage != nil {
		e.usage.InputTokens += result.Usage.InputTokens
		e.usage.OutputTokens += result.Usage.OutputTokens
		e.usage.TotalTokens += result.Usage.TotalTokens
		e.usage.CachedReadTokens += result.Usage.CachedReadTokens
		e.usage.ReasoningTokens += result.Usage.ReasoningTokens
	}
	switch e.format {
	case headlessOutputPlain:
		if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
			_, _ = fmt.Fprintln(e.output)
		}
		return nil
	case headlessOutputStreamingJSON:
	default:
		e.text.WriteString(result.Text)
	}
	return nil
}

func (e *headlessEmitter) finish(structured bool) error {
	result := e.last
	if e.format == headlessOutputPlain {
		return nil
	}
	if e.format == headlessOutputJSON {
		result.Text = e.text.String()
	}
	payload := map[string]any{
		"stopReason": "EndTurn", "sessionId": e.sessionID,
		"requestId": result.ResponseID,
	}
	if e.format == headlessOutputStreamingJSON {
		payload["type"] = "end"
	} else {
		payload["text"] = result.Text
	}
	attachHeadlessResult(payload, result, e.usage, e.turns, structured)
	encoder := json.NewEncoder(e.output)
	if e.format == headlessOutputJSON {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(payload)
}

func (e *headlessEmitter) emitError(err error) {
	if err == nil || e.format == headlessOutputPlain {
		return
	}
	_ = json.NewEncoder(e.output).Encode(map[string]any{"type": "error", "message": err.Error()})
}

func attachHeadlessResult(payload map[string]any, result agent.Result, usage api.Usage, turns int, structured bool) {
	if usage != (api.Usage{}) {
		inputTokens := usage.InputTokens - usage.CachedReadTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
		payload["usage"] = map[string]any{
			"input_tokens": inputTokens, "cache_read_input_tokens": usage.CachedReadTokens,
			"output_tokens": usage.OutputTokens, "reasoning_tokens": usage.ReasoningTokens,
			"total_tokens": usage.TotalTokens,
		}
		payload["num_turns"] = turns
	}
	if !structured {
		return
	}
	var value any
	if err := json.Unmarshal([]byte(result.Text), &value); err != nil {
		payload["structuredOutput"] = nil
		payload["structuredOutputError"] = "model did not produce valid JSON: " + err.Error()
		return
	}
	payload["structuredOutput"] = value
}
