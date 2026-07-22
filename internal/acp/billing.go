package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

type billingResponse struct {
	Config           *billingConfig `json:"config"`
	OnDemandEnabled  *bool          `json:"onDemandEnabled,omitempty"`
	SubscriptionTier *string        `json:"subscriptionTier,omitempty"`
}

type billingConfig struct {
	CreditUsagePercent   *float64             `json:"creditUsagePercent,omitempty"`
	CurrentPeriod        *usagePeriod         `json:"currentPeriod,omitempty"`
	MonthlyLimit         *cent                `json:"monthlyLimit,omitempty"`
	Used                 *cent                `json:"used,omitempty"`
	OnDemandCap          *cent                `json:"onDemandCap,omitempty"`
	OnDemandUsed         *cent                `json:"onDemandUsed,omitempty"`
	PrepaidBalance       *cent                `json:"prepaidBalance,omitempty"`
	IsUnifiedBillingUser *bool                `json:"isUnifiedBillingUser,omitempty"`
	BillingPeriodStart   *string              `json:"billingPeriodStart,omitempty"`
	BillingPeriodEnd     *string              `json:"billingPeriodEnd,omitempty"`
	History              []billingPeriodUsage `json:"history,omitempty"`
}

type usagePeriod struct {
	Type  *string `json:"type,omitempty"`
	Start *string `json:"start,omitempty"`
	End   *string `json:"end,omitempty"`
}

type cent struct {
	Val int64 `json:"val"`
}

type billingPeriodUsage struct {
	BillingCycle *billingCycle `json:"billingCycle,omitempty"`
	IncludedUsed *cent         `json:"includedUsed,omitempty"`
	OnDemandUsed *cent         `json:"onDemandUsed,omitempty"`
	TotalUsed    *cent         `json:"totalUsed,omitempty"`
}

type billingCycle struct {
	Year  int `json:"year"`
	Month int `json:"month"`
}

func (s *Server) handleBilling(ctx context.Context, incoming message) {
	credential, err := auth.Load(s.Auth.Path, s.Auth.Scope)
	if err != nil {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", billingAuthMessage(incoming.Method))
		return
	}
	token := credential.Key
	if s.Auth.TokenProvider != nil {
		token, err = s.Auth.TokenProvider(ctx, "")
	}
	if err != nil || token == "" {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", billingAuthMessage(incoming.Method))
		return
	}

	path, timeout := "/billing?format=credits", 15*time.Second
	if incoming.Method == "x.ai/auto-topup-rule" {
		path, timeout = "/auto-topup-rule", 10*time.Second
	}
	response, err := s.billingRequest(ctx, path, token, credential.UserID, timeout)
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", billingRequestError(incoming.Method, err))
		return
	}
	if response.StatusCode == http.StatusUnauthorized && s.Auth.TokenProvider != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = s.Auth.TokenProvider(ctx, token)
		if err != nil || token == "" {
			s.respondErrorData(incoming.ID, -32000, "Authentication required", billingAuthMessage(incoming.Method))
			return
		}
		response, err = s.billingRequest(ctx, path, token, credential.UserID, timeout)
		if err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", billingRequestError(incoming.Method, err))
			return
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", billingRequestError(incoming.Method, err))
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := fmt.Sprintf("HTTP %d", response.StatusCode)
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			if value, ok := payload["error"].(string); ok && value != "" {
				detail = value
			}
		}
		prefix := "Billing"
		if incoming.Method == "x.ai/auto-topup-rule" {
			prefix = "Auto top-up"
		}
		s.respondErrorData(incoming.ID, -32603, "Internal error", prefix+" service error: "+detail)
		return
	}

	if incoming.Method == "x.ai/auto-topup-rule" {
		var value any
		if json.Unmarshal(body, &value) != nil {
			value = map[string]any{"raw": string(body)}
		}
		s.respond(incoming.ID, value)
		return
	}
	var result billingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", "Failed to parse billing data: "+err.Error())
		return
	}
	if s.BillingMeta != nil {
		result.OnDemandEnabled, result.SubscriptionTier = s.BillingMeta()
	}
	s.respond(incoming.ID, result)
}

func (s *Server) billingRequest(ctx context.Context, path, token, userID string, timeout time.Duration) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(s.Auth.ProxyBaseURL, "/")+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
	request.Header.Set("x-userid", userID)
	request.Header.Set("x-grok-client-version", version.Current)
	request.Header.Set("x-grok-client-mode", "interactive")
	client := s.Auth.HTTP
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

func billingAuthMessage(method string) string {
	if method == "x.ai/auto-topup-rule" {
		return "Authentication required to fetch auto top-up rule"
	}
	return "Authentication required to fetch billing data"
}

func billingRequestError(method string, err error) string {
	if method == "x.ai/auto-topup-rule" {
		return "Failed to fetch auto top-up rule: " + err.Error()
	}
	return "Failed to fetch billing data: " + err.Error()
}
