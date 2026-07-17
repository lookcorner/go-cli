package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxSessionBytes = 64 << 20

type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Data any       `json:"data,omitempty"`
}

type Message struct {
	Role string
	Text string
}

type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func NewLogger(dir string) (*Logger, error) {
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
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(dir, id+".jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session log: %w", err)
	}
	return &Logger{file: file, path: path}, nil
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

// Transcript reconstructs the user/assistant messages through the last fully
// completed model turn. Events after that checkpoint are intentionally ignored
// because Resume continues from the same completed response ID.
func Transcript(path string) ([]Message, error) {
	if err := validateSessionFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var current, completed []Message
	line := 0
	for scanner.Scan() {
		line++
		var event struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse session line %d: %w", line, err)
		}
		switch event.Kind {
		case "user_prompt":
			var data struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("parse user prompt on session line %d: %w", line, err)
			}
			if data.Text != "" {
				current = append(current, Message{Role: "user", Text: data.Text})
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
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session log: %w", err)
	}
	if completed == nil {
		return nil, errors.New("session has no completed transcript to resume")
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
		parts = append(parts, label+"\n"+message.Text)
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
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	last := ""
	line := 0
	for scanner.Scan() {
		line++
		var event struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", fmt.Errorf("parse session line %d: %w", line, err)
		}
		if event.Kind != "model_response" {
			continue
		}
		var data struct {
			ResponseID    string `json:"response_id"`
			ToolCallCount int    `json:"tool_call_count"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return "", fmt.Errorf("parse model response on session line %d: %w", line, err)
		}
		if data.ResponseID != "" && data.ToolCallCount == 0 {
			last = data.ResponseID
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read session log: %w", err)
	}
	if last == "" {
		return "", errors.New("session has no completed model response to resume")
	}
	return last, nil
}

func (l *Logger) Path() string { return l.path }

func (l *Logger) Append(kind string, data any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
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
