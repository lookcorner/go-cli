package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

const maxSubscriptionResponseBytes = 1 << 20

var qualifyingSubscriptionTiers = map[string]struct{}{
	"SuperGrokPro":  {},
	"GrokPro":       {},
	"SuperGrokLite": {},
	"XPremiumPlus":  {},
	"XPremium":      {},
	"XBasic":        {},
}

// CheckSubscription reads the live subscription tier from the proxy. A
// successful request can still return qualifying=false for free or unknown tiers.
func CheckSubscription(ctx context.Context, baseURL, tokenHeader string, credential Credential, client *http.Client) (tier string, qualifying bool, err error) {
	if strings.TrimSpace(baseURL) == "" || credential.Key == "" {
		return "", false, errors.New("subscription base URL and credential are required")
	}
	if tokenHeader == "" {
		tokenHeader = DefaultTokenHeader
	}
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/user?include=subscription", nil)
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Authorization", "Bearer "+credential.Key)
	request.Header.Set("X-XAI-Token-Auth", tokenHeader)
	request.Header.Set("x-grok-client-version", version.Current)
	request.Header.Set("x-grok-client-mode", "interactive")
	response, err := client.Do(request)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", false, fmt.Errorf("subscription check failed: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionResponseBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > maxSubscriptionResponseBytes {
		return "", false, errors.New("subscription response exceeds 1 MiB")
	}
	var user struct {
		UserID           string  `json:"userId"`
		SubscriptionTier *string `json:"subscriptionTier"`
	}
	if err := json.Unmarshal(data, &user); err != nil {
		return "", false, fmt.Errorf("decode subscription response: %w", err)
	}
	if user.UserID == "" {
		return "", false, errors.New("subscription response is missing userId")
	}
	if user.SubscriptionTier == nil || *user.SubscriptionTier == "" {
		return "", false, nil
	}
	tier = *user.SubscriptionTier
	_, qualifying = qualifyingSubscriptionTiers[tier]
	return tier, qualifying, nil
}

func JWTTierClaim(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var claims map[string]any
	if decoder.Decode(&claims) != nil {
		return "", false
	}
	number, ok := claims["tier"].(json.Number)
	if !ok {
		return "", false
	}
	value, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return "", false
	}
	switch value {
	case 0:
		return "free", true
	case 1:
		return "supergrok", true
	case 2:
		return "x_basic", true
	case 3:
		return "x_premium", true
	case 4:
		return "x_premium_plus", true
	case 5:
		return "supergrok_heavy", true
	case 6:
		return "supergrok_lite", true
	default:
		return strconv.FormatUint(value, 10), true
	}
}

func JWTMatchesSubscriptionTier(token, subscriptionTier string) bool {
	claim, ok := JWTTierClaim(token)
	if !ok {
		return false
	}
	want := map[string]string{
		"GrokPro": "supergrok", "XBasic": "x_basic", "XPremium": "x_premium",
		"XPremiumPlus": "x_premium_plus", "SuperGrokPro": "supergrok_heavy", "SuperGrokLite": "supergrok_lite",
	}
	return want[subscriptionTier] != "" && claim == want[subscriptionTier]
}

func (c Credential) IsZDRTeam() bool {
	for _, reason := range c.TeamBlockedReasons {
		if reason == "BLOCKED_REASON_NO_LOGS" || reason == "BLOCKED_REASON_NO_LOGS_MODERATED" {
			return true
		}
	}
	return false
}
