package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type BtwEntry struct {
	BtwSessionID    string    `json:"btwSessionId"`
	ParentSessionID string    `json:"parentSessionId"`
	AskedAt         time.Time `json:"askedAt"`
	Question        string    `json:"question"`
	Answer          string    `json:"answer"`
	Model           string    `json:"model"`
	Success         bool      `json:"success"`
	Error           string    `json:"error,omitempty"`
}

var btwHistoryMu sync.Mutex

func AppendBtw(sessionPath string, entry BtwEntry) error {
	if err := validateSessionFile(sessionPath); err != nil {
		return err
	}
	entry.BtwSessionID = strings.TrimSpace(entry.BtwSessionID)
	entry.ParentSessionID = strings.TrimSpace(entry.ParentSessionID)
	entry.Question = strings.TrimSpace(entry.Question)
	if entry.BtwSessionID == "" || entry.ParentSessionID == "" || entry.Question == "" || entry.AskedAt.IsZero() {
		return errors.New("side question identity, parent, time, and question are required")
	}
	if entry.ParentSessionID != strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl") {
		return errors.New("side question parent does not match session path")
	}
	if entry.Success && strings.TrimSpace(entry.Answer) == "" {
		return errors.New("successful side question requires an answer")
	}
	if !entry.Success && strings.TrimSpace(entry.Error) == "" {
		return errors.New("failed side question requires an error")
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode side question: %w", err)
	}
	line = append(line, '\n')

	btwHistoryMu.Lock()
	defer btwHistoryMu.Unlock()
	path, err := BtwHistoryPath(sessionPath)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("side question history must be a regular, non-symlink file")
		}
		if info.Size()+int64(len(line)) > maxSessionBytes {
			return errors.New("side question history is too large")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open side question history: %w", err)
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return errors.New("side question history must be a regular file")
	}
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("append side question history: %w", err)
	}
	return file.Sync()
}

func BtwHistoryPath(sessionPath string) (string, error) {
	dir, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "btw_history.jsonl"), nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create side question directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("side question directory must be a non-symlink directory")
	}
	return nil
}
