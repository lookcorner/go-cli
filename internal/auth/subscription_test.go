package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/version"
)

func TestCheckSubscriptionRecognizesExactPaidTiers(t *testing.T) {
	tiers := []string{"SuperGrokPro", "GrokPro", "SuperGrokLite", "XPremiumPlus", "XPremium", "XBasic"}
	for _, tier := range tiers {
		t.Run(tier, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/user" || request.URL.Query().Get("include") != "subscription" {
					t.Errorf("URL=%s", request.URL.String())
				}
				for name, want := range map[string]string{
					"Authorization": "Bearer token", "X-XAI-Token-Auth": "token-header",
					"x-grok-client-version": version.Current, "x-grok-client-mode": "interactive",
				} {
					if got := request.Header.Get(name); got != want {
						t.Errorf("header %s=%q want %q", name, got, want)
					}
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"userId": "user-1", "subscriptionTier": tier})
			}))
			defer server.Close()
			got, qualifying, err := CheckSubscription(context.Background(), server.URL+"/v1", "token-header", Credential{Key: "token"}, server.Client())
			if err != nil || !qualifying || got != tier {
				t.Fatalf("tier=%q qualifying=%v err=%v", got, qualifying, err)
			}
		})
	}
}

func TestCheckSubscriptionRejectsMissingFreeAndPartialTiers(t *testing.T) {
	for _, tier := range []any{nil, "", "Free", "Super", "Grok", "XPremium+", " GrokPro"} {
		t.Run(fmt.Sprint(tier), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(writer).Encode(map[string]any{"userId": "user-1", "subscriptionTier": tier})
			}))
			defer server.Close()
			got, qualifying, err := CheckSubscription(context.Background(), server.URL, "", Credential{Key: "token"}, server.Client())
			if err != nil || qualifying {
				t.Fatalf("tier=%q qualifying=%v err=%v", got, qualifying, err)
			}
		})
	}
}

func TestCheckSubscriptionRejectsHTTPMalformedAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
	}{
		{name: "http", code: http.StatusUnauthorized, body: "denied"},
		{name: "malformed", body: "{"},
		{name: "missing user", body: `{"subscriptionTier":"GrokPro"}`},
		{name: "oversized", body: strings.Repeat("x", maxSubscriptionResponseBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.code != 0 {
					writer.WriteHeader(test.code)
				}
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			if _, _, err := CheckSubscription(context.Background(), server.URL, "", Credential{Key: "token"}, server.Client()); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
	if _, _, err := CheckSubscription(context.Background(), "", "", Credential{}, nil); err == nil {
		t.Fatal("missing request inputs were accepted")
	}
}

func TestJWTTierClaimAndSubscriptionMatch(t *testing.T) {
	jwt := func(tier any) string {
		payload, err := json.Marshal(map[string]any{"tier": tier})
		if err != nil {
			t.Fatal(err)
		}
		return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	}
	tests := []struct {
		value any
		claim string
		live  string
	}{
		{value: 0, claim: "free"},
		{value: 1, claim: "supergrok", live: "GrokPro"},
		{value: 2, claim: "x_basic", live: "XBasic"},
		{value: 3, claim: "x_premium", live: "XPremium"},
		{value: 4, claim: "x_premium_plus", live: "XPremiumPlus"},
		{value: 5, claim: "supergrok_heavy", live: "SuperGrokPro"},
		{value: 6, claim: "supergrok_lite", live: "SuperGrokLite"},
		{value: 99, claim: "99"},
	}
	for _, test := range tests {
		token := jwt(test.value)
		claim, ok := JWTTierClaim(token)
		if !ok || claim != test.claim {
			t.Fatalf("tier=%v claim=%q ok=%v", test.value, claim, ok)
		}
		if test.live != "" && !JWTMatchesSubscriptionTier(token, test.live) {
			t.Fatalf("tier=%v did not match %q", test.value, test.live)
		}
	}
	for _, token := range []string{"", "opaque", "a.bad.c", jwt("1"), jwt(1.5)} {
		if _, ok := JWTTierClaim(token); ok {
			t.Fatalf("invalid token accepted: %q", token)
		}
	}
	if JWTMatchesSubscriptionTier(jwt(2), "GrokPro") || JWTMatchesSubscriptionTier(jwt(5), "Unknown") {
		t.Fatal("stale or unknown tier matched")
	}
}

func TestCredentialIsZDRTeam(t *testing.T) {
	if !(Credential{TeamBlockedReasons: []string{"OTHER", "BLOCKED_REASON_NO_LOGS_MODERATED"}}).IsZDRTeam() {
		t.Fatal("moderated no-logs team was not recognized")
	}
	if (Credential{TeamBlockedReasons: []string{"OTHER"}}).IsZDRTeam() {
		t.Fatal("ordinary team was recognized as ZDR")
	}
}
