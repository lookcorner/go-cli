package changelog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

const (
	DefaultBaseURL = "https://x.ai/cli/changelogs"
	maxSize        = 4 << 20
)

var ErrUnavailable = errors.New("No release notes available (offline).")

type Service struct {
	CachePath     string
	JSONCachePath string
	BaseURL       string
	Version       string
	HTTP          *http.Client
}

func (s Service) Fetch(ctx context.Context) (string, error) {
	result := s.FetchAll(ctx)
	return available(result.Markdown)
}

type Result struct {
	Markdown string
	Bullets  []string
}

type Entry struct {
	Category       string `json:"category"`
	Description    string `json:"description"`
	BreakingChange bool   `json:"breaking_change"`
}

func (s Service) FetchAll(ctx context.Context) Result {
	markdown := make(chan string, 1)
	bullets := make(chan []string, 1)
	go func() { markdown <- s.fetchMarkdown(ctx) }()
	go func() { bullets <- s.fetchBullets(ctx) }()
	return Result{Markdown: <-markdown, Bullets: <-bullets}
}

func (s Service) fetchMarkdown(ctx context.Context) string {
	cached := s.readCache(s.CachePath)
	if offline() {
		return cached
	}
	content := s.fetch(ctx, ".external.md")
	if content != "" {
		s.writeCache(s.CachePath, content+"\n")
		return content
	}
	return cached
}

func IsCommand(input string) bool {
	fields := strings.Fields(input)
	return len(fields) > 0 && (fields[0] == "/release-notes" || fields[0] == "/changelog")
}

func (s Service) fetchBullets(ctx context.Context) []string {
	cached := s.readEntries()
	if offline() {
		return Bullets(cached, 3)
	}
	content := s.fetch(ctx, ".external.json")
	var entries []Entry
	if content != "" && json.Unmarshal([]byte(content), &entries) == nil {
		s.writeCache(s.JSONCachePath, content+"\n")
		return Bullets(entries, 3)
	}
	return Bullets(cached, 3)
}

func (s Service) fetch(ctx context.Context, suffix string) string {
	current := strings.TrimSpace(s.Version)
	if current == "" {
		current = version.Current
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+current+suffix, nil)
	if err != nil {
		return ""
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil || len(data) > maxSize {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s Service) readEntries() []Entry {
	content := s.readCache(s.JSONCachePath)
	var entries []Entry
	if json.Unmarshal([]byte(content), &entries) != nil {
		return nil
	}
	return entries
}

func (s Service) readCache(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s Service) writeCache(path, content string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte(content), 0o600)
	}
}

func Bullets(entries []Entry, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, min(len(entries), max(limit, 0)))
	for _, entry := range entries {
		description := strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(entry.Description))
		if description == "" {
			continue
		}
		result = append(result, description)
		if len(result) == limit {
			break
		}
	}
	return result
}

func available(content string) (string, error) {
	if content == "" {
		return "", ErrUnavailable
	}
	return content, nil
}

func offline() bool {
	value := os.Getenv("GROK_CHANGELOG_OFFLINE")
	return value != "" && value != "0"
}
