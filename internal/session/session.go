package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Data any       `json:"data,omitempty"`
}

type Logger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func NewLogger(dir string) (*Logger, error) {
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolve cache directory: %w", err)
		}
		dir = filepath.Join(cache, "gork-go", "sessions")
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
