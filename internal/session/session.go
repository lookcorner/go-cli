package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSessionBytes = 64 << 20
const maxImageBytes = 20 << 20

var validSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Data any       `json:"data,omitempty"`
}

type storedEvent struct {
	Time time.Time       `json:"time"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type RewindPoint struct {
	PromptIndex      int     `json:"prompt_index"`
	CreatedAt        string  `json:"created_at"`
	NumFileSnapshots int     `json:"num_file_snapshots"`
	HasFileChanges   bool    `json:"has_file_changes"`
	PromptPreview    *string `json:"prompt_preview"`
}

type RewindResult struct {
	TargetPromptIndex  int
	PreviousResponseID string
	PromptText         string
	Messages           []Message
}

// Fork copies a validated session event stream to a new session ID. A target
// keeps that prompt and its completed turn, but omits later prompts.
func Fork(dir, sourceID, newID, cwd, modelID string, target *int) (chatMessages, updates int, err error) {
	source, err := PathForID(dir, sourceID)
	if err != nil {
		return 0, 0, err
	}
	if err := validateSessionFile(source); err != nil {
		return 0, 0, err
	}
	dest, err := PathForID(dir, newID)
	if err != nil {
		return 0, 0, err
	}
	in, err := os.Open(source)
	if err != nil {
		return 0, 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("create forked session: %w", err)
	}
	cleanup := true
	defer func() {
		_ = out.Close()
		if cleanup {
			_ = os.Remove(dest)
		}
	}()
	limited := io.LimitReader(in, maxSessionBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxSessionBytes {
		return 0, 0, errors.New("source session is too large")
	}
	if target != nil {
		events, starts, _, timelineErr := liveTimeline(source)
		if timelineErr != nil {
			return 0, 0, timelineErr
		}
		if *target < 0 || *target >= len(starts) {
			return 0, 0, fmt.Errorf("cannot fork at prompt #%d; valid targets are 0..%d", *target, len(starts)-1)
		}
		cut := len(events)
		if *target+1 < len(starts) {
			cut = starts[*target+1]
		}
		var selected bytes.Buffer
		for _, event := range events[:cut] {
			encoded, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return 0, 0, marshalErr
			}
			selected.Write(encoded)
			selected.WriteByte('\n')
		}
		data = selected.Bytes()
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return 0, 0, errors.New("malformed source session event")
		}
		switch event.Kind {
		case "user_prompt", "model_response":
			chatMessages++
		default:
			updates++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if _, err := out.Write(data); err != nil {
		return 0, 0, err
	}
	metadata := map[string]any{"cwd": cwd}
	if modelID != "" {
		metadata["modelId"] = modelID
	}
	forked := map[string]any{"parent_session_id": sourceID}
	if target != nil {
		forked["target_prompt_index"] = *target
	}
	for _, event := range []Event{
		{Time: time.Now().UTC(), Kind: "session_metadata", Data: metadata},
		{Time: time.Now().UTC(), Kind: "session_forked", Data: forked},
	} {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return 0, 0, marshalErr
		}
		if _, err := out.Write(append(encoded, '\n')); err != nil {
			return 0, 0, err
		}
	}
	if err := out.Sync(); err != nil {
		return 0, 0, err
	}
	if err := out.Close(); err != nil {
		return 0, 0, err
	}
	cleanup = false
	return chatMessages, updates, nil
}

type Message struct {
	Role    string
	Text    string
	Content []Content
}

type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"-"`
}

type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func NewLogger(dir string) (*Logger, error) {
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	return NewLoggerWithID(dir, id)
}

func NewLoggerWithID(dir, id string) (*Logger, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	if !validSessionID.MatchString(id) {
		return nil, errors.New("invalid session ID")
	}
	path := filepath.Join(dir, id+".jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session log: %w", err)
	}
	return &Logger{file: file, path: path}, nil
}

func PathForID(dir, id string) (string, error) {
	if !validSessionID.MatchString(id) {
		return "", errors.New("invalid session ID")
	}
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, id+".jsonl"), nil
}

type Info struct {
	SessionID  string    `json:"sessionId"`
	CWD        string    `json:"cwd"`
	HeadCommit string    `json:"headCommit,omitempty"`
	ModelID    string    `json:"modelId,omitempty"`
	Title      string    `json:"title,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func List(dir, cwd string) ([]Info, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session directory: %w", err)
	}
	items := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if !validSessionID.MatchString(id) {
			continue
		}
		info, readErr := readInfo(filepath.Join(dir, entry.Name()), id)
		if readErr != nil || info.CWD == "" || (cwd != "" && info.CWD != cwd) {
			continue
		}
		items = append(items, info)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func readInfo(path, id string) (Info, error) {
	if err := validateSessionFile(path); err != nil {
		return Info{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer file.Close()
	stat, _ := file.Stat()
	info := Info{SessionID: id}
	if stat != nil {
		info.UpdatedAt = stat.ModTime().UTC()
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return Info{}, errors.New("malformed session event")
		}
		switch event.Kind {
		case "session_metadata":
			var data struct {
				CWD        string `json:"cwd"`
				HeadCommit string `json:"headCommit"`
				ModelID    string `json:"modelId"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.CWD != "" {
				info.CWD = data.CWD
				if data.HeadCommit != "" {
					info.HeadCommit = data.HeadCommit
				}
				if data.ModelID != "" {
					info.ModelID = data.ModelID
				}
			}
		case "user_prompt":
			if info.Title == "" {
				var data struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(event.Data, &data) == nil {
					info.Title = titleFromText(data.Text)
				}
			}
		}
	}
	return info, scanner.Err()
}

func titleFromText(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	runes := []rune(line)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return line
}

func DefaultDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory: %w", err)
	}
	return filepath.Join(cache, "gork-go", "sessions"), nil
}

// Resume opens an existing session log for append and returns the most recent
// response ID from a completed model turn (a response with no pending tools).
func Resume(path string) (*Logger, string, error) {
	if err := validateSessionFile(path); err != nil {
		return nil, "", err
	}
	responseID, err := lastCompletedResponseID(path)
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open session log for resume: %w", err)
	}
	logger := &Logger{file: file, path: path}
	if err := logger.Append("session_resumed", map[string]any{"previous_response_id": responseID}); err != nil {
		file.Close()
		return nil, "", err
	}
	return logger, responseID, nil
}

func Latest(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return "", err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read session directory: %w", err)
	}
	latest := ""
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		if entry.Name() > latest {
			latest = entry.Name()
		}
	}
	if latest == "" {
		return "", errors.New("no session logs found")
	}
	return filepath.Join(dir, latest), nil
}

// RewindPoints returns every prompt on the current append-only timeline.
func RewindPoints(path string) ([]RewindPoint, error) {
	events, starts, _, err := liveTimeline(path)
	if err != nil {
		return nil, err
	}
	points := make([]RewindPoint, 0, len(starts))
	for index, start := range starts {
		text, err := promptText(events[start])
		if err != nil {
			return nil, err
		}
		created := ""
		if !events[start].Time.IsZero() {
			created = events[start].Time.Format(time.RFC3339)
		}
		preview := promptPreview(text)
		var prompt *string
		if preview != "" {
			prompt = &preview
		}
		points = append(points, RewindPoint{PromptIndex: index, CreatedAt: created, PromptPreview: prompt})
	}
	return points, nil
}

// PreviewRewind resolves a conversation checkpoint without changing the log.
func PreviewRewind(path string, target int) (RewindResult, error) {
	events, starts, _, err := liveTimeline(path)
	if err != nil {
		return RewindResult{}, err
	}
	if target < 0 || target >= len(starts) {
		return RewindResult{}, fmt.Errorf("cannot rewind to prompt #%d; valid targets are 0..%d", target, len(starts)-1)
	}
	cut := starts[target]
	text, err := promptText(events[cut])
	if err != nil {
		return RewindResult{}, err
	}
	messages, err := transcriptFromEvents(path, events[:cut], true)
	if err != nil {
		return RewindResult{}, err
	}
	previous, err := completedResponseID(events[:cut])
	if err != nil {
		return RewindResult{}, err
	}
	return RewindResult{
		TargetPromptIndex: target, PreviousResponseID: previous,
		PromptText: text, Messages: messages,
	}, nil
}

// Rewind appends a branch marker and leaves the discarded branch intact for
// audit and future migrations. Only conversation state is changed here.
func Rewind(path string, target int) (RewindResult, error) {
	result, err := PreviewRewind(path, target)
	if err != nil {
		return RewindResult{}, err
	}
	event := Event{Time: time.Now().UTC(), Kind: "session_rewind", Data: map[string]any{
		"target_prompt_index": target, "previous_response_id": result.PreviousResponseID,
	}}
	encoded, err := json.Marshal(event)
	if err != nil {
		return RewindResult{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return RewindResult{}, fmt.Errorf("open session log for rewind: %w", err)
	}
	opened, statErr := file.Stat()
	linked, linkErr := os.Lstat(path)
	if statErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() || !os.SameFile(opened, linked) {
		_ = file.Close()
		return RewindResult{}, errors.New("session log changed before rewind")
	}
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return RewindResult{}, fmt.Errorf("append session rewind: %w", err)
	}
	return result, nil
}

func liveTimeline(path string) ([]storedEvent, []int, bool, error) {
	if err := validateSessionFile(path); err != nil {
		return nil, nil, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	var events []storedEvent
	var starts []int
	rewound := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	line := 0
	for scanner.Scan() {
		line++
		var event storedEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, nil, false, fmt.Errorf("parse session line %d: %w", line, err)
		}
		if event.Kind == "session_rewind" {
			var data struct {
				Target *int `json:"target_prompt_index"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil || data.Target == nil || *data.Target < 0 || *data.Target > len(starts) {
				return nil, nil, false, fmt.Errorf("invalid rewind marker on session line %d", line)
			}
			cut := len(events)
			if *data.Target < len(starts) {
				cut = starts[*data.Target]
			}
			events = events[:cut]
			starts = starts[:*data.Target]
			rewound = true
			continue
		}
		if event.Kind == "user_prompt" {
			starts = append(starts, len(events))
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("read session log: %w", err)
	}
	return events, starts, rewound, nil
}

func promptText(event storedEvent) (string, error) {
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return "", err
	}
	return data.Text, nil
}

func promptPreview(text string) string {
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			line = candidate
			break
		}
	}
	chars := []rune(line)
	if len(chars) > 60 {
		return string(chars[:57]) + "..."
	}
	return line
}

// Transcript reconstructs the user/assistant messages through the last fully
// completed model turn. Events after that checkpoint are intentionally ignored
// because Resume continues from the same completed response ID.
func Transcript(path string) ([]Message, error) {
	events, _, rewound, err := liveTimeline(path)
	if err != nil {
		return nil, err
	}
	return transcriptFromEvents(path, events, rewound)
}

func transcriptFromEvents(path string, events []storedEvent, allowEmpty bool) ([]Message, error) {
	var current, completed []Message
	for index, event := range events {
		line := index + 1
		switch event.Kind {
		case "user_prompt":
			var data struct {
				Text    string    `json:"text"`
				Content []Content `json:"content"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("parse user prompt on session line %d: %w", line, err)
			}
			if data.Text != "" || len(data.Content) > 0 {
				current = append(current, Message{Role: "user", Text: data.Text, Content: data.Content})
			}
		case "model_response":
			var data struct {
				ResponseID    string `json:"response_id"`
				Text          string `json:"text"`
				ToolCallCount int    `json:"tool_call_count"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("parse model response on session line %d: %w", line, err)
			}
			if data.Text != "" {
				if len(current) > 0 && current[len(current)-1].Role == "assistant" {
					current[len(current)-1].Text += data.Text
				} else {
					current = append(current, Message{Role: "assistant", Text: data.Text})
				}
			}
			if data.ResponseID != "" && data.ToolCallCount == 0 {
				completed = append([]Message(nil), current...)
			}
		}
	}
	if completed == nil {
		if allowEmpty {
			return []Message{}, nil
		}
		return nil, errors.New("session has no completed transcript to resume")
	}
	for messageIndex := range completed {
		for contentIndex := range completed[messageIndex].Content {
			part := &completed[messageIndex].Content[contentIndex]
			switch part.Type {
			case "text":
			case "image":
				if err := loadImage(path, part); err != nil {
					return nil, fmt.Errorf("load transcript image: %w", err)
				}
			default:
				return nil, fmt.Errorf("unsupported transcript content type %q", part.Type)
			}
		}
	}
	return completed, nil
}

func FormatTranscript(messages []Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		label := "Gork"
		if message.Role == "user" {
			label = "You"
		}
		body := message.Text
		if len(message.Content) > 0 {
			var content []string
			for _, part := range message.Content {
				switch part.Type {
				case "text":
					content = append(content, part.Text)
				case "image":
					if strings.HasPrefix(part.URI, "http://") || strings.HasPrefix(part.URI, "https://") {
						content = append(content, "[Image: "+part.URI+"]")
					} else {
						content = append(content, "[Image]")
					}
				}
			}
			body = strings.Join(content, "\n")
		}
		parts = append(parts, label+"\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

func validateSessionFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat session log: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("session log must be a regular, non-symlink file")
	}
	if info.Size() > maxSessionBytes {
		return fmt.Errorf("session log exceeds %d bytes", maxSessionBytes)
	}
	return nil
}

func lastCompletedResponseID(path string) (string, error) {
	events, _, rewound, err := liveTimeline(path)
	if err != nil {
		return "", err
	}
	last, err := completedResponseID(events)
	if err != nil {
		return "", err
	}
	if last == "" {
		if rewound {
			return "", nil
		}
		return "", errors.New("session has no completed model response to resume")
	}
	return last, nil
}

func completedResponseID(events []storedEvent) (string, error) {
	last := ""
	for index, event := range events {
		if event.Kind != "model_response" {
			continue
		}
		var data struct {
			ResponseID    string `json:"response_id"`
			ToolCallCount int    `json:"tool_call_count"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return "", fmt.Errorf("parse model response on live event %d: %w", index+1, err)
		}
		if data.ResponseID != "" && data.ToolCallCount == 0 {
			last = data.ResponseID
		}
	}
	return last, nil
}

func CompletedResponseID(path string) (string, error) { return lastCompletedResponseID(path) }

func (l *Logger) Path() string { return l.path }

func ArtifactDir(sessionPath string) (string, error) {
	base := filepath.Base(sessionPath)
	if filepath.Ext(base) != ".jsonl" {
		return "", errors.New("session path must end in .jsonl")
	}
	id := strings.TrimSuffix(base, ".jsonl")
	if !validSessionID.MatchString(id) {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(filepath.Dir(sessionPath), "artifacts", id), nil
}

func (l *Logger) Append(kind string, data any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(kind, data)
}

func (l *Logger) AppendPrompt(text string, content []Content) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	persisted := make([]Content, 0, len(content))
	var created []string
	for _, part := range content {
		switch part.Type {
		case "text":
			if part.Text != "" {
				persisted = append(persisted, Content{Type: "text", Text: part.Text})
			}
		case "image":
			stored, path, err := l.persistImage(part.URI)
			if err != nil {
				removeFiles(created)
				return err
			}
			persisted = append(persisted, stored)
			if path != "" {
				created = append(created, path)
			}
		default:
			removeFiles(created)
			return fmt.Errorf("unsupported prompt content type %q", part.Type)
		}
	}
	data := struct {
		Text    string    `json:"text"`
		Content []Content `json:"content,omitempty"`
	}{Text: text, Content: persisted}
	if err := l.appendLocked("user_prompt", data); err != nil {
		removeFiles(created)
		return err
	}
	return nil
}

func (l *Logger) appendLocked(kind string, data any) error {
	if l.file == nil {
		return nil
	}
	event := Event{Time: time.Now().UTC(), Kind: kind, Data: data}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := l.file.Write(encoded); err != nil {
		return fmt.Errorf("write session event: %w", err)
	}
	return l.file.Sync()
}

func (l *Logger) persistImage(rawURL string) (Content, string, error) {
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			return Content{}, "", errors.New("image has an invalid HTTP(S) URI")
		}
		return Content{Type: "image", URI: rawURL}, "", nil
	}
	if !strings.HasPrefix(rawURL, "data:") {
		return Content{}, "", errors.New("image must use a base64 data URL or HTTP(S) URI")
	}
	mediaType, encoded, ok := strings.Cut(strings.TrimPrefix(rawURL, "data:"), ";base64,")
	if !ok {
		return Content{}, "", errors.New("image must use a base64 data URL or HTTP(S) URI")
	}
	ext, ok := imageExtension(mediaType)
	if !ok {
		return Content{}, "", fmt.Errorf("unsupported image mime type %q", mediaType)
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > maxImageBytes {
		return Content{}, "", errors.New("image exceeds 20 MB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !validImage(mediaType, data) {
		return Content{}, "", errors.New("image data does not match its mime type")
	}
	assets := filepath.Join(filepath.Dir(l.path), "assets")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		return Content{}, "", fmt.Errorf("create session assets: %w", err)
	}
	assetsInfo, err := os.Lstat(assets)
	if err != nil || assetsInfo.Mode()&os.ModeSymlink != 0 || !assetsInfo.IsDir() {
		return Content{}, "", errors.New("session assets must be a non-symlink directory")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Content{}, "", fmt.Errorf("name session image: %w", err)
	}
	name := "image-" + hex.EncodeToString(random) + "." + ext
	path := filepath.Join(assets, name)
	temporary, err := os.CreateTemp(assets, ".image-*")
	if err != nil {
		return Content{}, "", fmt.Errorf("create session image: %w", err)
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tempPath, path)
	}
	if err != nil {
		return Content{}, "", fmt.Errorf("save session image: %w", err)
	}
	return Content{Type: "image", URI: filepath.ToSlash(filepath.Join("assets", name)), MimeType: mediaType}, path, nil
}

func loadImage(sessionPath string, content *Content) error {
	if strings.HasPrefix(content.URI, "http://") || strings.HasPrefix(content.URI, "https://") {
		parsed, err := url.Parse(content.URI)
		if err != nil || parsed.Host == "" {
			return errors.New("invalid remote image URI")
		}
		return nil
	}
	clean := filepath.Clean(filepath.FromSlash(content.URI))
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.Dir(clean) != "assets" {
		return errors.New("invalid session image path")
	}
	assets := filepath.Join(filepath.Dir(sessionPath), "assets")
	assetsInfo, err := os.Lstat(assets)
	if err != nil || assetsInfo.Mode()&os.ModeSymlink != 0 || !assetsInfo.IsDir() {
		return errors.New("session assets must be a non-symlink directory")
	}
	path := filepath.Join(filepath.Dir(sessionPath), clean)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxImageBytes {
		return errors.New("session image must be a regular file no larger than 20 MB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, ok := imageExtension(content.MimeType); !ok || !validImage(content.MimeType, data) {
		return errors.New("session image data does not match its mime type")
	}
	content.Data = base64.StdEncoding.EncodeToString(data)
	return nil
}

func imageExtension(mediaType string) (string, bool) {
	switch mediaType {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}

func validImage(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return len(data) >= 4 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}) && bytes.Equal(data[len(data)-2:], []byte{0xff, 0xd9})
	case "image/gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
}

func removeFiles(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
