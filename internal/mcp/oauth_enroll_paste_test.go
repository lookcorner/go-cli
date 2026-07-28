package mcp

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseMCPCallbackURLAndFields(t *testing.T) {
	t.Parallel()
	got := parseMCPCallback("http://127.0.0.1:9/callback?code=abc&state=xyz&iss=https://as.example")
	if got.err != nil || got.code != "abc" || got.state != "xyz" || got.iss != "https://as.example" {
		t.Fatalf("url=%#v", got)
	}
	fields := parseMCPCallback("abc xyz")
	if fields.err != nil || fields.code != "abc" || fields.state != "xyz" {
		t.Fatalf("fields=%#v", fields)
	}
	denied := parseMCPCallback("http://127.0.0.1/callback?error=access_denied&error_description=nope")
	if denied.err == nil || !strings.Contains(denied.err.Error(), "nope") {
		t.Fatalf("denied=%#v", denied)
	}
	if bare := parseMCPCallback("only-code"); bare.err == nil {
		t.Fatal("expected bare code rejection")
	}
}

func TestSubmitMCPAuthCallbackDeliversPaste(t *testing.T) {
	t.Parallel()
	if err := SubmitMCPAuthCallback("missing", "http://x/callback?code=a&state=b"); err == nil {
		t.Fatal("expected inactive enroll error")
	}
	ch := make(chan oauthCallback, 1)
	unregister := registerMCPAuthSubmit("linear", ch)
	defer unregister()
	if err := SubmitMCPAuthCallback("linear", "http://127.0.0.1/callback?code=pasted&state=st"); err != nil {
		t.Fatal(err)
	}
	if err := SubmitMCPAuthCallback("linear", "http://127.0.0.1/callback?code=again&state=st"); err == nil {
		t.Fatal("expected already submitted")
	}
	select {
	case got := <-ch:
		if got.err != nil || got.code != "pasted" || got.state != "st" {
			t.Fatalf("got=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestAuthenticateMCPServerPastedInput(t *testing.T) {
	server, path := oauthTestFixture(t)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	result, err := AuthenticateMCPServer(ctx, "linear", server.URL+"/rpc", AuthenticateOpts{
		CredentialsPath: path,
		HTTPClient:      server.Client(),
		Force:           true,
		PastedInput:     reader,
		OpenURL: func(authURL string) bool {
			parsed, err := url.Parse(authURL)
			if err != nil {
				_ = writer.CloseWithError(err)
				return false
			}
			state := parsed.Query().Get("state")
			go func() {
				time.Sleep(50 * time.Millisecond)
				_, _ = fmt.Fprintf(writer, "http://127.0.0.1/callback?code=auth-code&state=%s\n", url.QueryEscape(state))
				_ = writer.Close()
			}()
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Credentials.AccessToken() != "access-token" {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthenticateMCPServerSubmitDuringEnroll(t *testing.T) {
	server, path := oauthTestFixture(t)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := AuthenticateMCPServer(ctx, "linear", server.URL+"/rpc", AuthenticateOpts{
		CredentialsPath: path,
		HTTPClient:      server.Client(),
		Force:           true,
		OpenURL: func(authURL string) bool {
			parsed, err := url.Parse(authURL)
			if err != nil {
				return false
			}
			state := parsed.Query().Get("state")
			go func() {
				time.Sleep(50 * time.Millisecond)
				_ = SubmitMCPAuthCallback("linear", "http://127.0.0.1/callback?code=auth-code&state="+url.QueryEscape(state))
			}()
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Credentials.AccessToken() != "access-token" {
		t.Fatalf("result=%#v", result)
	}
}
