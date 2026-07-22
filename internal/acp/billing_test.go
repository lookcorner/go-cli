package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/version"
)

func TestBillingReturnsReferenceShapeAndMetadata(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.RequestURI() != "/billing?format=credits" {
			t.Errorf("request method=%s URI=%s", request.Method, request.URL.RequestURI())
		}
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-XAI-Token-Auth") != auth.DefaultTokenHeader || request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-grok-client-version") != version.Current || request.Header.Get("x-grok-client-mode") != "interactive" {
			t.Errorf("request headers=%#v", request.Header)
		}
		_, _ = writer.Write([]byte(`{"config":{"creditUsagePercent":42.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-06-01T00:00:00Z","end":"2026-06-08T00:00:00Z"},"monthlyLimit":{"val":2000},"used":{},"onDemandCap":{"val":500},"prepaidBalance":{"val":1250},"isUnifiedBillingUser":true,"history":[{"billingCycle":{"year":2026,"month":5},"includedUsed":{"val":1800},"onDemandUsed":{},"totalUsed":{"val":1800}}],"productUsage":[{"ignored":true}]}}`))
	}))
	defer upstream.Close()
	onDemand, tier := true, "SuperGrok Heavy"
	var output bytes.Buffer
	server := &Server{
		output: &output, BillingMeta: func() (*bool, *string) { return &onDemand, &tier },
		Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client()},
	}
	server.handleBilling(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/billing"})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	config := result["config"].(map[string]any)
	history := config["history"].([]any)[0].(map[string]any)
	if requests != 1 || result["onDemandEnabled"] != true || result["subscriptionTier"] != tier || config["creditUsagePercent"] != 42.5 || config["prepaidBalance"].(map[string]any)["val"] != float64(1250) || config["used"].(map[string]any)["val"] != float64(0) || history["billingCycle"].(map[string]any)["month"] != float64(5) {
		t.Fatalf("result=%#v requests=%d", result, requests)
	}
	if _, ok := config["productUsage"]; ok {
		t.Fatalf("unknown backend field leaked: %#v", config)
	}
}

func TestAutoTopupRuleReturnsUpstreamJSONAndRawFallback(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	body := `{"rule":{"topupAmount":{"val":500},"minBeforeHittingSl":{}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/auto-topup-rule" {
			t.Errorf("path=%q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(body))
	}))
	defer upstream.Close()
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client()}}
	server.handleBilling(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auto-topup-rule"})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if result["rule"].(map[string]any)["topupAmount"].(map[string]any)["val"] != float64(500) {
		t.Fatalf("result=%#v", result)
	}

	rawUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("not-json")) }))
	defer rawUpstream.Close()
	server.Auth.ProxyBaseURL, server.Auth.HTTP = rawUpstream.URL, rawUpstream.Client()
	output.Reset()
	server.handleBilling(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auto-topup-rule"})
	result = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if result["raw"] != "not-json" {
		t.Fatalf("raw fallback=%#v", result)
	}
}

func TestBillingRefreshesOnceAfterUnauthorized(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "old", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokens = append(tokens, request.Header.Get("Authorization"))
		if len(tokens) == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"config":null}`))
	}))
	defer upstream.Close()
	provider := func(_ context.Context, rejected string) (string, error) {
		if rejected == "" {
			return "old", nil
		}
		if rejected != "old" {
			t.Fatalf("rejected token=%q", rejected)
		}
		return "new", nil
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client(), TokenProvider: provider}}
	server.handleBilling(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/billing"})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if len(tokens) != 2 || tokens[0] != "Bearer old" || tokens[1] != "Bearer new" || result["config"] != nil {
		t.Fatalf("tokens=%#v result=%#v", tokens, result)
	}
}

type billingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f billingRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestBillingErrors(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope}}
	server.handleBilling(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/billing"})
	errorValue := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32000) || errorValue["message"] != "Authentication required" || errorValue["data"] != "Authentication required to fetch billing data" {
		t.Fatalf("missing auth=%#v", errorValue)
	}
	if err := auth.Save(path, scope, auth.Credential{Key: "token", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}

	server.Auth.TokenProvider = func(context.Context, string) (string, error) { return "", errors.New("refresh failed") }
	output.Reset()
	server.handleBilling(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auto-topup-rule"})
	errorValue = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32000) || errorValue["message"] != "Authentication required" || errorValue["data"] != "Authentication required to fetch auto top-up rule" {
		t.Fatalf("provider error=%#v", errorValue)
	}

	server.Auth.TokenProvider = nil
	server.Auth.ProxyBaseURL = "https://proxy.example"
	server.Auth.HTTP = &http.Client{Transport: billingRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}
	output.Reset()
	server.handleBilling(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/billing"})
	errorValue = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	errorData := errorValue["data"].(string)
	if errorValue["code"] != float64(-32603) || errorValue["message"] != "Internal error" || !strings.HasPrefix(errorData, "Failed to fetch billing data:") || !strings.Contains(errorData, "offline") {
		t.Fatalf("transport error=%#v", errorValue)
	}

	for _, test := range []struct {
		status int
		body   string
		want   string
	}{
		{status: http.StatusServiceUnavailable, body: `{"error":"credits unavailable"}`, want: "Billing service error: credits unavailable"},
		{status: http.StatusBadGateway, body: `{"message":"ignored"}`, want: "Billing service error: HTTP 502"},
		{status: http.StatusOK, body: `{`, want: "Failed to parse billing data:"},
	} {
		server.Auth.HTTP = &http.Client{Transport: billingRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
		})}
		output.Reset()
		server.handleBilling(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/billing"})
		errorValue = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
		if errorValue["code"] != float64(-32603) || errorValue["message"] != "Internal error" || !strings.HasPrefix(errorValue["data"].(string), test.want) {
			t.Fatalf("status=%d error=%#v", test.status, errorValue)
		}
	}
}

func TestBillingExtensionsRouteThroughServer(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/billing" {
			_, _ = writer.Write([]byte(`{"config":null}`))
			return
		}
		_, _ = writer.Write([]byte(`{"rule":null}`))
	}))
	defer upstream.Close()
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/billing","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/auto-topup-rule","params":{}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client()},
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	billing := decodeACP(t, decoder)
	topup := decodeACP(t, decoder)
	if billing["id"] != float64(1) || billing["result"].(map[string]any)["config"] != nil || topup["id"] != float64(2) || topup["result"].(map[string]any)["rule"] != nil {
		t.Fatalf("billing=%#v topup=%#v", billing, topup)
	}
}
