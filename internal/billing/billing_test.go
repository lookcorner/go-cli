package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		prompt  string
		action  CommandAction
		handled bool
	}{
		{"/usage", ShowUsage, true},
		{" /usage show ", ShowUsage, true},
		{"/cost", ShowUsage, true},
		{"/usage manage", ManageUsage, true},
		{"/cost manage", ManageUsage, true},
		{"/usage SHOW", InvalidUsage, true},
		{"/usage other value", InvalidUsage, true},
		{"/usagex", 0, false},
		{"usage", 0, false},
	}
	for _, test := range tests {
		command, handled := ParseCommand(test.prompt)
		if handled != test.handled || command.Action != test.action {
			t.Errorf("prompt=%q command=%#v handled=%v", test.prompt, command, handled)
		}
		if command.Action == InvalidUsage && (!strings.Contains(command.Message, "/usage show") || !strings.Contains(command.Message, "/usage manage")) {
			t.Errorf("invalid message=%q", command.Message)
		}
	}
}

func TestFormatUsageUsesCreditsShapeAndAutoTopup(t *testing.T) {
	usage := 42.9
	periodType := "USAGE_PERIOD_TYPE_WEEKLY"
	end := "2026-06-08T20:00:00Z"
	prepaid := int64(-1250)
	config := Config{
		CreditUsagePercent: &usage,
		CurrentPeriod:      &UsagePeriod{Type: &periodType, End: &end},
		PrepaidBalance:     &Cent{Val: prepaid},
	}
	autoTopup := map[string]any{"rule": map[string]any{
		"enabled": true, "topupAmount": map[string]any{"val": -2000}, "maxAmountPerMonth": map[string]any{"val": -10000},
	}}
	wantReset := time.Date(2026, 6, 8, 20, 0, 0, 0, time.UTC).Local().Format("January 2, 15:04")
	result := FormatUsage(config, autoTopup)
	for _, want := range []string{"Weekly limit: 42%", "Next reset: " + wantReset, "Credits: $12.50", "Auto topup: $20", "Max monthly topup: $100"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in %q", want, result)
		}
	}
}

func TestFormatUsageFallsBackToLegacyBilling(t *testing.T) {
	monthly := "USAGE_PERIOD_TYPE_MONTHLY"
	result := FormatUsage(Config{
		CurrentPeriod: &UsagePeriod{Type: &monthly},
		MonthlyLimit:  &Cent{Val: 10000}, Used: &Cent{Val: 12500},
		OnDemandCap: &Cent{Val: 5000},
	}, nil)
	if result != "Monthly limit: 100%\n\nPay-as-you-go: $25.00 used of $50.00 limit" {
		t.Fatalf("result=%q", result)
	}
}

func TestFormatUsageClampsAndDisablesUnknownTopup(t *testing.T) {
	negative, over := -10.0, 150.0
	if got := FormatUsage(Config{CreditUsagePercent: &negative}, nil); got != "Usage: 0%" {
		t.Fatalf("negative=%q", got)
	}
	if got := FormatUsage(Config{CreditUsagePercent: &over, PrepaidBalance: &Cent{Val: 453}}, map[string]any{"rule": nil}); got != "Usage: 100%\n\nCredits: $4.53\nAuto topup: disabled" {
		t.Fatalf("over=%q", got)
	}
	invalidEnd := "not-a-date"
	if got := FormatUsage(Config{BillingPeriodEnd: &invalidEnd}, nil); got != "Usage: 0%" {
		t.Fatalf("invalid end=%q", got)
	}
}

func TestUsageFetchesBillingAndAutoTopup(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", UserID: "user-1", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	requests := make(map[string]int)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/billing":
			_, _ = writer.Write([]byte(`{"config":{"creditUsagePercent":42.9,"prepaidBalance":{"val":-1250}}}`))
		case "/auto-topup-rule":
			_, _ = writer.Write([]byte(`{"rule":{"enabled":true,"topupAmount":{"val":-2000}}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()
	service := Service{AuthPath: path, AuthScope: scope, BaseURL: upstream.URL, HTTP: upstream.Client()}
	result, err := service.Usage(context.Background())
	if err != nil || result != "Usage: 42%\n\nCredits: $12.50\nAuto topup: $20" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if requests["/billing"] != 1 || requests["/auto-topup-rule"] != 1 {
		t.Fatalf("requests=%#v", requests)
	}
}
