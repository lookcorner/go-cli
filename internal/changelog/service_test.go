package changelog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceFetchesAndCachesReleaseNotes(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache", "CHANGELOG.md")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/1.2.3.external.md" {
			t.Errorf("path=%q", request.URL.Path)
		}
		_, _ = writer.Write([]byte("\n# Release notes\n\n- Added sessions\n"))
	}))
	defer server.Close()

	service := Service{CachePath: cache, BaseURL: server.URL, Version: "1.2.3", HTTP: server.Client()}
	content, err := service.Fetch(context.Background())
	if err != nil || content != "# Release notes\n\n- Added sessions" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	data, err := os.ReadFile(cache)
	if err != nil || string(data) != content+"\n" {
		t.Fatalf("cache=%q err=%v", data, err)
	}
}

func TestServiceUsesCacheOfflineAndAfterRemoteFailure(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(cache, []byte("# Cached notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	service := Service{CachePath: cache, BaseURL: server.URL, Version: "1", HTTP: server.Client()}

	t.Setenv("GROK_CHANGELOG_OFFLINE", "1")
	content, err := service.Fetch(context.Background())
	if err != nil || content != "# Cached notes" || requests != 0 {
		t.Fatalf("offline content=%q err=%v requests=%d", content, err, requests)
	}
	t.Setenv("GROK_CHANGELOG_OFFLINE", "0")
	content, err = service.Fetch(context.Background())
	if err != nil || content != "# Cached notes" || requests != 1 {
		t.Fatalf("fallback content=%q err=%v requests=%d", content, err, requests)
	}
}

func TestServiceReturnsReferenceErrorWithoutReleaseNotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("  \n"))
	}))
	defer server.Close()
	service := Service{CachePath: filepath.Join(t.TempDir(), "missing.md"), BaseURL: server.URL, HTTP: server.Client()}
	if content, err := service.Fetch(context.Background()); content != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestIsCommand(t *testing.T) {
	for _, input := range []string{"/release-notes", "/release-notes ignored", " /changelog "} {
		if !IsCommand(input) {
			t.Errorf("command not recognized: %q", input)
		}
	}
	for _, input := range []string{"", "/release-notesx", "/changelogs"} {
		if IsCommand(input) {
			t.Errorf("non-command recognized: %q", input)
		}
	}
	if !strings.Contains(ErrUnavailable.Error(), "offline") {
		t.Fatalf("error=%q", ErrUnavailable)
	}
}
