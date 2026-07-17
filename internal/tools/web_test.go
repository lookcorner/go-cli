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
