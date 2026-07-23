package changelog

import (
	"context"
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
	CachePath string
	BaseURL   string
	Version   string
	HTTP      *http.Client
}

func (s Service) Fetch(ctx context.Context) (string, error) {
	cached := s.readCache()
	if offline() {
		return available(cached)
	}

	current := strings.TrimSpace(s.Version)
	if current == "" {
		current = version.Current
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+current+".external.md", nil)
	if err != nil {
		return available(cached)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return available(cached)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return available(cached)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil || len(data) > maxSize {
		return available(cached)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return available(cached)
	}
	s.writeCache(content + "\n")
	return content, nil
}

func IsCommand(input string) bool {
	fields := strings.Fields(input)
	return len(fields) > 0 && (fields[0] == "/release-notes" || fields[0] == "/changelog")
}

func (s Service) readCache() string {
	if strings.TrimSpace(s.CachePath) == "" {
		return ""
	}
	data, err := os.ReadFile(s.CachePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s Service) writeCache(content string) {
	if strings.TrimSpace(s.CachePath) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.CachePath), 0o700); err == nil {
		_ = os.WriteFile(s.CachePath, []byte(content), 0o600)
	}
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
