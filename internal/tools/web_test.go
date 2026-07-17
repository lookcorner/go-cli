package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type webRoundTripFunc func(*http.Request) (*http.Response, error)

func (f webRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestWebFetchReadsBoundedTextAndRejectsPrivateByDefault(t *testing.T) {
	fixtureURL := "http://127.0.0.1/page"
	if _, err := validateFetchURL(context.Background(), fixtureURL, false); err == nil {
		t.Fatal("loopback URL was accepted")
	}
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" {
			t.Fatalf("web_fetch did not upgrade HTTP to HTTPS: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/plain"}},
			Body:   io.NopCloser(strings.NewReader("fixture page")),
		}, nil
	})}
	tool := &webFetchTool{
		approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true,
	}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+fixtureURL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "fixture page") || !strings.Contains(output, "Content-Type: text/plain") {
		t.Fatalf("unexpected fetch output: %q", output)
	}
}

func TestWebFetchURLValidationBounds(t *testing.T) {
	if _, err := validateFetchURL(context.Background(), "https://localhost/page", true); err == nil || !strings.Contains(err.Error(), "single-label") {
		t.Fatalf("unexpected single-label error: %v", err)
	}
	tooLong := "https://example.com/" + strings.Repeat("x", maxWebFetchURL)
	if _, err := validateFetchURL(context.Background(), tooLong, true); err == nil || !strings.Contains(err.Error(), "exceeds 2000") {
		t.Fatalf("unexpected URL length error: %v", err)
	}
}

func TestWebFetchStopsAtCrossHostRedirect(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found", Request: request,
			Header: http.Header{"Location": []string{"https://other.example/result"}},
			Body:   io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/start"}`))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(output, "cross-host redirect from example.com to https://other.example/result") {
		t.Fatalf("unexpected redirect output=%q requests=%d", output, requests)
	}
}

func TestWebFetchFollowsSameHostRedirectIgnoringWWW(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound, Status: "302 Found", Request: request,
				Header: http.Header{"Location": []string{"https://www.example.com/final"}},
				Body:   io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/plain"}},
			Body:   io.NopCloser(strings.NewReader("redirected content")),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/start"}`))
	if err != nil || requests != 2 || !strings.Contains(output, "redirected content") || !strings.Contains(output, "https://www.example.com/final") {
		t.Fatalf("unexpected same-host redirect: output=%q requests=%d err=%v", output, requests, err)
	}
}

func TestWebFetchLimitsRedirects(t *testing.T) {
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found", Request: request,
			Header: http.Header{"Location": []string{"/again"}}, Body: io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/start"}`)); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("unexpected redirect limit error: %v", err)
	}
}

func TestWebFetchCachesSuccessfulText(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/plain"}},
			Body:   io.NopCloser(strings.NewReader("cached page")),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	for range 2 {
		output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"http://example.com/page"}`))
		if err != nil || !strings.Contains(output, "cached page") {
			t.Fatalf("unexpected cached fetch: output=%q err=%v", output, err)
		}
	}
	if requests != 1 {
		t.Fatalf("cache made %d HTTP requests, want 1", requests)
	}
}

func TestWebFetchCacheExpiresAndEvictsOldest(t *testing.T) {
	cache := webFetchCache{ttl: time.Minute, max: 2}
	start := time.Unix(1_700_000_000, 0)
	cache.put("a", "first", start)
	cache.put("b", "second", start.Add(time.Second))
	cache.put("c", "third", start.Add(2*time.Second))
	if _, ok := cache.get("a", start.Add(3*time.Second)); ok {
		t.Fatal("oldest cache entry was not evicted")
	}
	if output, ok := cache.get("b", start.Add(30*time.Second)); !ok || output != "second" {
		t.Fatalf("unexpected live cache entry: output=%q ok=%v", output, ok)
	}
	if _, ok := cache.get("b", start.Add(62*time.Second)); ok {
		t.Fatal("expired cache entry was returned")
	}
}

func TestWebFetchDomainPermissionRule(t *testing.T) {
	base := &recordingApprover{}
	approver, err := NewPolicyApprover(base, base,
		[]string{"WebFetchDomain(example.com)"}, nil,
		[]string{"WebFetchDomain(blocked.example.com)"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "web fetch", "https://example.com/page"); err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "web fetch", "https://blocked.example.com/page"); err == nil {
		t.Fatal("domain deny rule was not enforced")
	}
}

func TestWebFetchConvertsHTMLToMarkdown(t *testing.T) {
	fixtureURL := "https://example.com/page"
	html := `<!doctype html><html><head><title>ignored</title><script>secret()</script></head><body>
<h1>Project</h1><p>Hello <strong>world</strong>. <a href="/docs">Read docs</a>.</p>
<ol><li>First</li><li>Second</li></ol><pre><code>go test ./...
done</code></pre><img alt="inline" src="data:image/png;base64,secret"></body></html>`
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:   io.NopCloser(strings.NewReader(html)),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+fixtureURL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Content-Type: markdown", "# Project", "Hello **world**.", "[Read docs](https://example.com/docs)", "1. First", "2. Second", "```\ngo test ./...\ndone\n```"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("markdown output missing %q:\n%s", expected, output)
		}
	}
	for _, forbidden := range []string{"<h1>", "secret()", "base64,secret"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("markdown output retained %q:\n%s", forbidden, output)
		}
	}
}

func TestWebFetchRejectsBinaryContent(t *testing.T) {
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:   io.NopCloser(strings.NewReader("\x00binary")),
		}, nil
	})}
	tool := &webFetchTool{approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/file"}`)); err == nil || !strings.Contains(err.Error(), "unsupported web content type") {
		t.Fatalf("unexpected binary response error: %v", err)
	}
}

func TestWebFetchPersistsOverflowArtifactAndSkipsCache(t *testing.T) {
	body := strings.Repeat("complete artifact line\n", 1000)
	requests := 0
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/plain"}},
			Body:   io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts", "session-1")
	tool := &webFetchTool{
		approver: PromptApprover{Mode: PermissionAuto}, client: client, allowPrivate: true,
		artifactDir: artifactRoot, contextWindow: 1000,
	}
	for call := 1; call <= 2; call++ {
		output, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/large"}`))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(artifactRoot, "web_fetch", fmt.Sprintf("%d.txt", call))
		if !strings.Contains(output, "web_fetch content truncated") || !strings.Contains(output, path) || strings.Contains(output, body) {
			t.Fatalf("unexpected overflow output: %q", output)
		}
		data, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil {
			t.Fatalf("readErr=%v statErr=%v", err, statErr)
		}
		if string(data) != body || info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact=%q mode=%v", data, info.Mode().Perm())
		}
	}
	if requests != 2 {
		t.Fatalf("truncated fetch was cached: requests=%d", requests)
	}
	dirInfo, err := os.Stat(filepath.Join(artifactRoot, "web_fetch"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("artifact directory mode=%v", dirInfo.Mode().Perm())
	}
	workspaceRoot := t.TempDir()
	ws, _ := workspace.Open(workspaceRoot)
	reader := &readFileTool{ws: ws, artifactRoot: artifactRoot}
	read, err := reader.Execute(context.Background(), json.RawMessage(`{"target_file":"`+filepath.Join(artifactRoot, "web_fetch", "1.txt")+`","offset":2,"limit":2}`))
	if err != nil || !strings.Contains(read, "complete artifact line") {
		t.Fatalf("artifact recovery=%q err=%v", read, err)
	}
}

func TestWebFetchArtifactRejectsSymlinkedRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "artifacts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := &webFetchTool{artifactDir: filepath.Join(root, "artifacts", "session")}
	if _, err := tool.saveWebArtifact([]byte("secret"), "text/plain"); err == nil {
		t.Fatal("symlinked artifact root was accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("artifact escaped session storage: %#v", entries)
	}
}

func TestReadFileAbsoluteWorkspacePathBeforeArtifactsExist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspace.txt")
	if err := os.WriteFile(path, []byte("workspace content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(root)
	reader := &readFileTool{ws: ws, artifactRoot: filepath.Join(t.TempDir(), "artifacts", "session")}
	output, err := reader.Execute(context.Background(), json.RawMessage(`{"target_file":"`+path+`"}`))
	if err != nil || !strings.Contains(output, "workspace content") {
		t.Fatalf("absolute workspace read=%q err=%v", output, err)
	}
}

func TestBoundedWebContentPreservesUTF8(t *testing.T) {
	content := strings.Repeat("é", 100)
	output := boundedWebContent(content, 31, "")
	if !utf8.ValidString(output) || !strings.Contains(output, "showing first 30 of 200 bytes") {
		t.Fatalf("invalid UTF-8 truncation: %q", output)
	}
	if webArtifactExtension("text/plain", []byte(`{"ok":true}`)) != "json" || webArtifactExtension("markdown", nil) != "md" {
		t.Fatal("web artifact extension classification failed")
	}
}
