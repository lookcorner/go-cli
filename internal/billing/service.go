package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

type Response struct {
	Config           *Config `json:"config"`
	OnDemandEnabled  *bool   `json:"onDemandEnabled,omitempty"`
	SubscriptionTier *string `json:"subscriptionTier,omitempty"`
}

type Config struct {
	CreditUsagePercent   *float64      `json:"creditUsagePercent,omitempty"`
	CurrentPeriod        *UsagePeriod  `json:"currentPeriod,omitempty"`
	MonthlyLimit         *Cent         `json:"monthlyLimit,omitempty"`
	Used                 *Cent         `json:"used,omitempty"`
	OnDemandCap          *Cent         `json:"onDemandCap,omitempty"`
	OnDemandUsed         *Cent         `json:"onDemandUsed,omitempty"`
	PrepaidBalance       *Cent         `json:"prepaidBalance,omitempty"`
	IsUnifiedBillingUser *bool         `json:"isUnifiedBillingUser,omitempty"`
	BillingPeriodStart   *string       `json:"billingPeriodStart,omitempty"`
	BillingPeriodEnd     *string       `json:"billingPeriodEnd,omitempty"`
	History              []PeriodUsage `json:"history,omitempty"`
}

type UsagePeriod struct {
	Type  *string `json:"type,omitempty"`
	Start *string `json:"start,omitempty"`
	End   *string `json:"end,omitempty"`
}

type Cent struct {
	Val int64 `json:"val"`
}

type PeriodUsage struct {
	BillingCycle *Cycle `json:"billingCycle,omitempty"`
	IncludedUsed *Cent  `json:"includedUsed,omitempty"`
	OnDemandUsed *Cent  `json:"onDemandUsed,omitempty"`
	TotalUsed    *Cent  `json:"totalUsed,omitempty"`
}

type Cycle struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

type Service struct {
	AuthPath      string
	AuthScope     string
	BaseURL       string
	HTTP          *http.Client
	TokenProvider api.TokenProvider
	Metadata      func() (*bool, *string)
}

type Error struct {
	Authentication bool
	Message        string
}

func (e *Error) Error() string { return e.Message }

func (s Service) FetchBilling(ctx context.Context) (Response, error) {
	body, err := s.fetch(ctx, "/billing?format=credits", 15*time.Second, "Billing", "billing data")
	if err != nil {
		return Response{}, err
	}
	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		return Response{}, &Error{Message: "Failed to parse billing data: " + err.Error()}
	}
	if s.Metadata != nil {
		result.OnDemandEnabled, result.SubscriptionTier = s.Metadata()
	}
	return result, nil
}

func (s Service) FetchAutoTopup(ctx context.Context) (any, error) {
	body, err := s.fetch(ctx, "/auto-topup-rule", 10*time.Second, "Auto top-up", "auto top-up rule")
	if err != nil {
		return nil, err
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return map[string]any{"raw": string(body)}, nil
	}
	return value, nil
}

func (s Service) Usage(ctx context.Context) (string, error) {
	result, err := s.FetchBilling(ctx)
	if err != nil {
		return "", err
	}
	if result.Config == nil {
		return "No billing data available.", nil
	}
	topup, _ := s.FetchAutoTopup(ctx)
	return FormatUsage(*result.Config, topup), nil
}

func (s Service) fetch(ctx context.Context, path string, timeout time.Duration, serviceName, operation string) ([]byte, error) {
	credential, err := auth.Load(s.AuthPath, s.AuthScope)
	if err != nil {
		return nil, &Error{Authentication: true, Message: "Authentication required to fetch " + operation}
	}
	token := credential.Key
	if s.TokenProvider != nil {
		token, err = s.TokenProvider(ctx, "")
	}
	if err != nil || token == "" {
		return nil, &Error{Authentication: true, Message: "Authentication required to fetch " + operation}
	}

	response, err := s.request(ctx, path, token, credential.UserID, timeout)
	if err != nil {
		return nil, &Error{Message: "Failed to fetch " + operation + ": " + err.Error()}
	}
	if response.StatusCode == http.StatusUnauthorized && s.TokenProvider != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = s.TokenProvider(ctx, token)
		if err != nil || token == "" {
			return nil, &Error{Authentication: true, Message: "Authentication required to fetch " + operation}
		}
		response, err = s.request(ctx, path, token, credential.UserID, timeout)
		if err != nil {
			return nil, &Error{Message: "Failed to fetch " + operation + ": " + err.Error()}
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, &Error{Message: "Failed to fetch " + operation + ": " + err.Error()}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := fmt.Sprintf("HTTP %d", response.StatusCode)
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			if value, ok := payload["error"].(string); ok && value != "" {
				detail = value
			}
		}
		return nil, &Error{Message: serviceName + " service error: " + detail}
	}
	return body, nil
}

func (s Service) request(ctx context.Context, path, token, userID string, timeout time.Duration) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
	request.Header.Set("x-userid", userID)
	request.Header.Set("x-grok-client-version", version.Current)
	request.Header.Set("x-grok-client-mode", "interactive")
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelReadCloser{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelReadCloser) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func FormatUsage(config Config, autoTopup any) string {
	limit, used := centValue(config.MonthlyLimit), centValue(config.Used)
	usage := 0.0
	if config.CreditUsagePercent != nil {
		usage = *config.CreditUsagePercent
	} else if limit > 0 {
		usage = float64(used) / float64(limit) * 100
	}
	usage = math.Max(0, math.Min(100, usage))
	label := "Usage"
	periodType := ""
	if config.CurrentPeriod != nil && config.CurrentPeriod.Type != nil {
		periodType = *config.CurrentPeriod.Type
	}
	if strings.Contains(periodType, "WEEKLY") {
		label = "Weekly limit"
	} else if strings.Contains(periodType, "MONTHLY") {
		label = "Monthly limit"
	}
	lines := []string{fmt.Sprintf("%s: %.0f%%", label, math.Floor(usage))}
	if reset := periodEnd(config); reset != "" {
		lines = append(lines, "Next reset: "+reset)
	}
	if prepaid := absCents(centValue(config.PrepaidBalance)); prepaid > 0 {
		lines = append(lines, "", "Credits: "+formatDollars(prepaid))
		enabled, amount, maximum := parseAutoTopup(autoTopup)
		if enabled && amount != 0 {
			lines = append(lines, "Auto topup: "+formatDollars(absCents(amount)))
			if maximum != 0 {
				lines = append(lines, "Max monthly topup: "+formatDollars(absCents(maximum)))
			}
		} else {
			lines = append(lines, "Auto topup: disabled")
		}
	}
	if cap := centValue(config.OnDemandCap); cap > 0 {
		onDemandUsed := centValue(config.OnDemandUsed)
		if config.OnDemandUsed == nil {
			onDemandUsed = max(used-limit, 0)
		}
		lines = append(lines, "", fmt.Sprintf("Pay-as-you-go: $%.2f used of $%.2f limit", math.Abs(float64(onDemandUsed))/100, math.Abs(float64(cap))/100))
	}
	return strings.Join(lines, "\n")
}

func periodEnd(config Config) string {
	value := ""
	if config.CurrentPeriod != nil && config.CurrentPeriod.End != nil {
		value = *config.CurrentPeriod.End
	} else if config.BillingPeriodEnd != nil {
		value = *config.BillingPeriodEnd
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.Local().Format("January 2, 15:04")
}

func parseAutoTopup(value any) (bool, int64, int64) {
	body, err := json.Marshal(value)
	if err != nil {
		return false, 0, 0
	}
	var response struct {
		Rule *struct {
			Enabled           bool  `json:"enabled"`
			TopupAmount       *Cent `json:"topupAmount"`
			MaxAmountPerMonth *Cent `json:"maxAmountPerMonth"`
		} `json:"rule"`
	}
	if json.Unmarshal(body, &response) != nil || response.Rule == nil {
		return false, 0, 0
	}
	return response.Rule.Enabled, centValue(response.Rule.TopupAmount), centValue(response.Rule.MaxAmountPerMonth)
}

func centValue(value *Cent) int64 {
	if value == nil {
		return 0
	}
	return value.Val
}

func absCents(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func formatDollars(cents int64) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%d", cents/100)
	}
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}
