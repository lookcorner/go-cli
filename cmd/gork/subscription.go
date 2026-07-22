package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

type acpSubscriptionChecker struct {
	mu            sync.Mutex
	authPath      string
	scope         string
	tokenProvider api.TokenProvider
	http          *http.Client
	config        func() config.Config
	applySettings func(*config.RemoteSettings)
	refreshModels func(string, string)
}

func (c *acpSubscriptionChecker) Check(ctx context.Context) acp.SubscriptionCheckResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg := c.config()
	if c.tokenProvider == nil {
		credential := auth.Credential{Key: cfg.APIKey, AuthMode: "api_key", CodingDataRetentionOptOut: true}
		if credential.Key == "" && cfg.DeploymentKey != "" {
			credential.Key, credential.AuthMode = cfg.DeploymentKey, "external"
		}
		if credential.Key == "" {
			return acp.SubscriptionCheckResult{}
		}
		return acp.SubscriptionCheckResult{Authenticated: true, Meta: acpAuthMeta(cfg, credential, true)}
	}
	credential, err := auth.Load(c.authPath, c.scope)
	if err != nil {
		return acp.SubscriptionCheckResult{}
	}
	if credential.AuthMode == "external" || credential.AuthMode == "api_key" {
		return acp.SubscriptionCheckResult{Authenticated: true, Meta: acpAuthMeta(cfg, credential, true)}
	}
	tier, qualifying, checkErr := auth.CheckSubscription(ctx, cfg.ProxyBaseURL, auth.DefaultTokenHeader, credential, c.http)
	if checkErr == nil && qualifying {
		token := credential.Key
		if c.tokenProvider != nil {
			if refreshed, refreshErr := c.tokenProvider(ctx, token); refreshErr == nil && refreshed != "" {
				token = refreshed
			}
		}
		settingsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		remote := config.FetchRemoteSettingsForSession(settingsCtx, cfg.ProxyBaseURL, token, credential.UserID, credential.Email, c.http)
		cancel()
		if remote != nil {
			if c.applySettings != nil {
				c.applySettings(remote)
			}
			cfg = c.config()
		}
		if cfg.AllowAccess != nil && *cfg.AllowAccess {
			if c.tokenProvider != nil {
				if refreshed, refreshErr := c.tokenProvider(ctx, token); refreshErr == nil && refreshed != "" {
					token = refreshed
				}
			}
			if c.refreshModels != nil {
				c.refreshModels(tier, token)
			}
		}
	}
	if latest, loadErr := auth.Load(c.authPath, c.scope); loadErr == nil {
		credential = latest
	}
	cfg = c.config()
	return acp.SubscriptionCheckResult{Authenticated: true, Meta: acpAuthMeta(cfg, credential, cfg.AllowAccess != nil && *cfg.AllowAccess)}
}

func acpAuthMeta(cfg config.Config, credential auth.Credential, allowed bool) *acp.AuthMeta {
	var gate *acp.AuthGate
	if cfg.GateMessage != nil && strings.TrimSpace(*cfg.GateMessage) != "" {
		gate = &acp.AuthGate{Message: *cfg.GateMessage, URL: cfg.GateURL, Label: cfg.GateLabel}
	} else if !allowed {
		url, label := "https://grok.com/supergrok?referrer=grok-build", "Subscribe"
		gate = &acp.AuthGate{Message: "A subscription is required.", URL: &url, Label: &label}
	}
	var tier *string
	if cfg.SubscriptionTierDisplay != nil && strings.TrimSpace(*cfg.SubscriptionTierDisplay) != "" {
		value := *cfg.SubscriptionTierDisplay
		tier = &value
	} else if credential.AuthMode == "api_key" {
		value := "api_key"
		tier = &value
	} else if value, ok := auth.JWTTierClaim(credential.Key); ok {
		tier = &value
	}
	mode := map[string]string{"oidc": "Oidc", "external": "External", "api_key": "ApiKey"}[credential.AuthMode]
	return &acp.AuthMeta{
		Email: optionalACPString(credential.Email), AuthMode: optionalACPString(mode),
		TeamID: optionalACPString(credential.TeamID), TeamName: optionalACPString(credential.TeamName),
		IsZDR: credential.IsZDRTeam(), TeamRole: optionalACPString(credential.TeamRole),
		CodingDataRetentionOptOut: credential.CodingDataRetentionOptOut,
		ShowResolvedModel:         cfg.ShowResolvedModel, Gate: gate, SubscriptionTier: tier,
	}
}

func optionalACPString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type acpSubscriptionCatalogRefresher struct {
	ctx           context.Context
	inFlight      atomic.Bool
	config        func() config.Config
	authPath      string
	scope         string
	tokenProvider api.TokenProvider
	reload        func() error
	retryDelays   []time.Duration
}

func (r *acpSubscriptionCatalogRefresher) Start(tier, token string) {
	if tier == "" || token == "" || r.tokenProvider == nil || !r.inFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.inFlight.Store(false)
		if auth.JWTMatchesSubscriptionTier(token, tier) {
			r.fetch(token)
			return
		}
		delays := r.retryDelays
		if delays == nil {
			delays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
		}
		for _, delay := range delays {
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(delay):
			}
			refreshed, err := r.tokenProvider(r.ctx, token)
			if err != nil || refreshed == "" {
				continue
			}
			token = refreshed
			if auth.JWTMatchesSubscriptionTier(token, tier) {
				r.fetch(token)
				return
			}
		}
	}()
}

func (r *acpSubscriptionCatalogRefresher) fetch(token string) {
	cfg := r.config()
	cfg.APIKey = token
	authMethod, origin := modelCacheIdentity(cfg, r.tokenProvider)
	if _, err := fetchACPModelCache(r.ctx, cfg, authMethod, origin, r.authPath, r.scope); err == nil && r.reload != nil {
		_ = r.reload()
	}
}
