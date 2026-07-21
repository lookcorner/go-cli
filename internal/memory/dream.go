package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxDreamInputBytes  = 32_000
	maxDreamOutputChars = 16_000
	dreamCleanupAge     = 5 * time.Minute
)

type DreamInput struct {
	Content  string
	Eligible int
	paths    []string
}

type DreamResult struct {
	Outcome  string
	Path     string
	Chars    int
	Eligible int
	Cleaned  int
}

func (s *Store) PrepareDream(config DreamConfig, manual bool) (DreamInput, DreamResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !manual && !config.Enabled {
		return DreamInput{}, DreamResult{Outcome: "disabled"}, nil
	}
	last, err := dreamLastTime(filepath.Join(s.workspaceDir, ".dream-lock"))
	if err != nil {
		return DreamInput{}, DreamResult{}, err
	}
	if !manual && !last.IsZero() && time.Since(last) < time.Duration(config.MinHours)*time.Hour {
		return DreamInput{}, DreamResult{Outcome: "too_soon"}, nil
	}
	entries, err := sessionEntries(s.sessionsDir)
	if err != nil {
		return DreamInput{}, DreamResult{}, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if (!last.IsZero() && !entry.modified.After(last)) || strings.Contains(filepath.Base(entry.path), "-"+s.sessionID+"-") {
			continue
		}
		paths = append(paths, entry.path)
	}
	sort.Strings(paths)
	if !manual && uint64(len(paths)) < config.MinSessions {
		return DreamInput{}, DreamResult{Outcome: "too_few_sessions", Eligible: len(paths)}, nil
	}
	if len(paths) == 0 {
		return DreamInput{}, DreamResult{Outcome: "nothing_to_consolidate"}, nil
	}

	var output strings.Builder
	memoryPath := filepath.Join(s.workspaceDir, "MEMORY.md")
	if data, readErr := readMemoryFile(memoryPath); readErr == nil && !dreamScaffold(string(data)) {
		text := strings.TrimSpace(string(data))
		if len(text) > maxDreamInputBytes/2 {
			text = truncateUTF8Bytes(text, maxDreamInputBytes/2)
		}
		if text != "" {
			output.WriteString("--- Existing Memory (merge with new sessions) ---\n\n" + text)
		}
	}
	processed := make([]string, 0, len(paths))
	for _, path := range paths {
		data, readErr := readMemoryFile(path)
		if readErr != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		if output.Len() > 0 {
			output.WriteString("\n\n")
		}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		fmt.Fprintf(&output, "--- Session: %s ---\n\n%s", stem, data)
		processed = append(processed, path)
		if output.Len() >= maxDreamInputBytes {
			break
		}
	}
	if len(processed) == 0 {
		return DreamInput{}, DreamResult{Outcome: "nothing_to_consolidate", Eligible: len(paths)}, nil
	}
	return DreamInput{Content: output.String(), Eligible: len(paths), paths: processed}, DreamResult{}, nil
}

func (s *Store) CommitDream(response string, input DreamInput, staleSeconds uint64) (DreamResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ephemeral {
		return DreamResult{Outcome: "nothing_to_consolidate", Eligible: input.Eligible}, nil
	}
	lockPath := filepath.Join(s.workspaceDir, ".dream-lock")
	prior, acquired, err := acquireDreamLock(lockPath, staleSeconds)
	if err != nil {
		return DreamResult{Outcome: "failed", Eligible: input.Eligible}, err
	}
	if !acquired {
		return DreamResult{Outcome: "lock_held", Eligible: input.Eligible}, nil
	}
	content := processDreamResponse(response)
	if content == "" {
		return DreamResult{Outcome: "nothing_to_consolidate", Eligible: input.Eligible}, nil
	}
	path := filepath.Join(s.workspaceDir, "MEMORY.md")
	if info, statErr := os.Lstat(path); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		_ = rollbackDreamLock(lockPath, prior)
		return DreamResult{Outcome: "failed", Eligible: input.Eligible}, errors.New("workspace memory must be a regular file")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		_ = rollbackDreamLock(lockPath, prior)
		return DreamResult{Outcome: "failed", Eligible: input.Eligible}, statErr
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = rollbackDreamLock(lockPath, prior)
		return DreamResult{Outcome: "failed", Eligible: input.Eligible}, err
	}
	cleaned := 0
	now := time.Now()
	for _, sessionPath := range input.paths {
		info, statErr := os.Lstat(sessionPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < dreamCleanupAge {
			continue
		}
		if err := os.Remove(sessionPath); err == nil {
			cleaned++
		}
	}
	return DreamResult{Outcome: "written", Path: path, Chars: len([]rune(content)), Eligible: input.Eligible, Cleaned: cleaned}, nil
}

func processDreamResponse(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || isNoReplyText(value) {
		return ""
	}
	valid := false
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") || strings.HasPrefix(strings.TrimSpace(line), "## ") {
			valid = true
			break
		}
	}
	if !valid {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxDreamOutputChars {
		runes = runes[:maxDreamOutputChars]
	}
	return string(runes)
}

func dreamScaffold(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) >= 500 {
		return false
	}
	for _, marker := range []string{"Auto-populated by dream consolidation", "Add project-specific knowledge here", "Add any cross-project preferences here"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return value == ""
}

func dreamLastTime(path string) (time.Time, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return time.Time{}, errors.New("dream lock must be a regular file")
	}
	return info.ModTime(), nil
}

func acquireDreamLock(path string, staleSeconds uint64) (*time.Time, bool, error) {
	var prior *time.Time
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, false, errors.New("dream lock must be a regular file")
		}
		value := info.ModTime()
		prior = &value
		data, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if time.Since(info.ModTime()) < time.Duration(staleSeconds)*time.Second && processAlive(pid) {
			return prior, false, nil
		}
		if err := os.Remove(path); err != nil {
			return prior, false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return prior, false, nil
		}
		return prior, false, err
	}
	if _, err = file.WriteString(strconv.Itoa(os.Getpid())); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = rollbackDreamLock(path, prior)
		return prior, false, err
	}
	return prior, true, nil
}

func rollbackDreamLock(path string, prior *time.Time) error {
	if prior == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}
	return os.Chtimes(path, *prior, *prior)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func isNoReplyText(value string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	return normalized == "noreply"
}
