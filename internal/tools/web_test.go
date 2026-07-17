package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type webRoundTripFunc func(*http.Request) (*http.Response, error)

func (f webRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestWebFetchReadsBoundedTextAndRejectsPrivateByDefault(t *testing.T) {
	fixtureURL := "http://127.0.0.1/page"
	if _, err := validateFetchURL(context.Background(), fixtureURL, false); err == nil {
		t.Fatal("loopback URL was accepted")
	}
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
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
