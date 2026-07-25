package changelog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestServiceFetchesAndCachesReleaseNotes(t *testing.T) {
	requireLoopback(t)
	directory := filepath.Join(t.TempDir(), "cache")
	cache, jsonCache := filepath.Join(directory, "CHANGELOG.md"), filepath.Join(directory, "CHANGELOG.json")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1.2.3.external.md":
			_, _ = writer.Write([]byte("\n# Release notes\n\n- Added sessions\n"))
		case "/1.2.3.external.json":
			_, _ = writer.Write([]byte(`[{"category":"features","description":"Added **sessions**","breaking_change":false}]`))
		default:
			t.Errorf("path=%q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := Service{CachePath: cache, JSONCachePath: jsonCache, BaseURL: server.URL, Version: "1.2.3", HTTP: server.Client()}
	result := service.FetchAll(context.Background())
	if result.Markdown != "# Release notes\n\n- Added sessions" || len(result.Bullets) != 1 || result.Bullets[0] != "Added sessions" {
		t.Fatalf("result=%#v", result)
	}
	data, err := os.ReadFile(cache)
	if err != nil || string(data) != result.Markdown+"\n" {
		t.Fatalf("cache=%q err=%v", data, err)
	}
	if data, err = os.ReadFile(jsonCache); err != nil || !strings.Contains(string(data), "Added **sessions**") {
		t.Fatalf("json cache=%q err=%v", data, err)
	}
}

func TestServiceUsesCacheOfflineAndAfterRemoteFailure(t *testing.T) {
	requireLoopback(t)
	cache := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(cache, []byte("# Cached notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	service := Service{CachePath: cache, BaseURL: server.URL, Version: "1", HTTP: server.Client()}

	t.Setenv("GROK_CHANGELOG_OFFLINE", "1")
	content, err := service.Fetch(context.Background())
	if err != nil || content != "# Cached notes" || requests.Load() != 0 {
		t.Fatalf("offline content=%q err=%v requests=%d", content, err, requests.Load())
	}
	t.Setenv("GROK_CHANGELOG_OFFLINE", "0")
	content, err = service.Fetch(context.Background())
	if err != nil || content != "# Cached notes" || requests.Load() != 2 {
		t.Fatalf("fallback content=%q err=%v requests=%d", content, err, requests.Load())
	}
}

func TestServiceUsesValidCachedJSONAfterMalformedRemote(t *testing.T) {
	requireLoopback(t)
	directory := t.TempDir()
	jsonCache := filepath.Join(directory, "CHANGELOG.json")
	if err := os.WriteFile(jsonCache, []byte(`[{"description":"Cached **fix**"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = writer.Write([]byte("not json"))
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	service := Service{JSONCachePath: jsonCache, BaseURL: server.URL, Version: "1", HTTP: server.Client()}
	result := service.FetchAll(context.Background())
	if len(result.Bullets) != 1 || result.Bullets[0] != "Cached fix" {
		t.Fatalf("result=%#v", result)
	}
}

func TestBulletsSkipsEmptyStripsInlineMarkdownAndLimits(t *testing.T) {
	entries := []Entry{
		{Description: ""},
		{Description: "Added **dark mode**"},
		{Description: "Fixed `startup`"},
		{Description: "Faster rendering"},
	}
	if bullets := Bullets(entries, 2); len(bullets) != 2 || bullets[0] != "Added dark mode" || bullets[1] != "Fixed startup" {
		t.Fatalf("bullets=%#v", bullets)
	}
	if bullets := Bullets(entries, 0); bullets != nil {
		t.Fatalf("zero limit=%#v", bullets)
	}
}

func TestServiceReturnsReferenceErrorWithoutReleaseNotes(t *testing.T) {
	requireLoopback(t)
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
