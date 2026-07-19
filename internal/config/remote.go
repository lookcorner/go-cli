package config

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/compat"
)

type RemoteSettings struct {
	OfficialMarketplaceAutoRegister *bool    `json:"official_marketplace_auto_register"`
	WebFetchEnabled                 *bool    `json:"web_fetch_enabled"`
	AutoWakeEnabled                 *bool    `json:"auto_wake_enabled"`
	WebFetchProxy                   *string  `json:"web_fetch_proxy"`
	WebFetchAllowedDomains          []string `json:"web_fetch_allowed_domains"`
	CursorSkills                    *bool    `json:"cursor_skills_enabled"`
	CursorRules                     *bool    `json:"cursor_rules_enabled"`
	CursorAgents                    *bool    `json:"cursor_agents_enabled"`
	CursorMCPs                      *bool    `json:"cursor_mcps_enabled"`
	CursorHooks                     *bool    `json:"cursor_hooks_enabled"`
	ClaudeSkills                    *bool    `json:"claude_skills_enabled"`
	ClaudeRules                     *bool    `json:"claude_rules_enabled"`
	ClaudeAgents                    *bool    `json:"claude_agents_enabled"`
	ClaudeMCPs                      *bool    `json:"claude_mcps_enabled"`
	ClaudeHooks                     *bool    `json:"claude_hooks_enabled"`
}

func FetchRemoteSettings(ctx context.Context, baseURL, token string, client *http.Client) *RemoteSettings {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimRight(baseURL, "/") + "/settings"
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			var settings RemoteSettings
			err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&settings)
			response.Body.Close()
			if err == nil {
				return &settings
			}
			return nil
		}
		if response != nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			response.Body.Close()
			if response.StatusCode < 500 {
				return nil
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			}
		}
	}
	return nil
}

func (c *Config) ApplyRemoteSettings(remote *RemoteSettings) {
	if remote == nil {
		return
	}
	if remote.OfficialMarketplaceAutoRegister != nil {
		c.OfficialMarketplaceAutoRegister = *remote.OfficialMarketplaceAutoRegister
	}
	if value, ok := envBool("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER"); ok {
		c.OfficialMarketplaceAutoRegister = value
	}
	if !c.WebFetch.EnabledConfigured && remote.WebFetchEnabled != nil {
		c.WebFetch.Enabled = *remote.WebFetchEnabled
	}
	if !c.autoWakeConfigured && remote.AutoWakeEnabled != nil {
		c.AutoWakeEnabled = *remote.AutoWakeEnabled
	}
	if !c.WebFetch.ProxyConfigured && remote.WebFetchProxy != nil {
		c.WebFetch.ProxyEndpoint = *remote.WebFetchProxy
	}
	if !c.WebFetch.DomainsConfigured && remote.WebFetchAllowedDomains != nil {
		c.WebFetch.AllowedDomains = append([]string(nil), remote.WebFetchAllowedDomains...)
		c.WebFetch.DomainsConfigured = true
	}
	applyRemoteVendor(&c.Compat.Cursor, c.compatConfigured.Cursor, "CURSOR", remote.CursorSkills, remote.CursorRules, remote.CursorAgents, remote.CursorMCPs, remote.CursorHooks)
	applyRemoteVendor(&c.Compat.Claude, c.compatConfigured.Claude, "CLAUDE", remote.ClaudeSkills, remote.ClaudeRules, remote.ClaudeAgents, remote.ClaudeMCPs, remote.ClaudeHooks)
}

func applyRemoteVendor(target *compat.Vendor, configured compat.Vendor, vendor string, values ...*bool) {
	fields := []*bool{&target.Skills, &target.Rules, &target.Agents, &target.Mcps, &target.Hooks}
	configuredFields := []bool{configured.Skills, configured.Rules, configured.Agents, configured.Mcps, configured.Hooks}
	names := []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS"}
	for index, value := range values {
		if value != nil && !configuredFields[index] {
			if _, set := envBool("GROK_" + vendor + "_" + names[index] + "_ENABLED"); !set {
				*fields[index] = *value
			}
		}
	}
}
