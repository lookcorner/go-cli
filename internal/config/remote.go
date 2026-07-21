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
	OfficialMarketplaceAutoRegister *bool          `json:"official_marketplace_auto_register"`
	WebFetchEnabled                 *bool          `json:"web_fetch_enabled"`
	AutoWakeEnabled                 *bool          `json:"auto_wake_enabled"`
	TwoPassCompactionEnabled        *bool          `json:"two_pass_compaction_enabled"`
	MemoryEnabled                   *bool          `json:"memory_enabled"`
	MemoryInitialInjectionEnabled   *bool          `json:"memory_initial_injection_enabled"`
	MemorySearchMaxResults          *int           `json:"memory_search_max_results"`
	MemorySearchMinScore            *float64       `json:"memory_search_min_score"`
	MemoryTemporalDecayEnabled      *bool          `json:"memory_temporal_decay_enabled"`
	MemoryTemporalDecayHalfLifeDays *float64       `json:"memory_temporal_decay_half_life_days"`
	MemoryMMREnabled                *bool          `json:"memory_mmr_enabled"`
	MemoryMMRLambda                 *float64       `json:"memory_mmr_lambda"`
	DreamEnabled                    *bool          `json:"dream_enabled"`
	DreamMinHours                   *uint64        `json:"dream_min_hours"`
	DreamMinSessions                *uint64        `json:"dream_min_sessions"`
	DreamCheckIntervalSeconds       *uint64        `json:"dream_check_interval_secs"`
	FlushEnabled                    *bool          `json:"flush_enabled"`
	FlushSoftThresholdTokens        *int           `json:"flush_soft_threshold_tokens"`
	FlushIdleTimeoutSeconds         *uint64        `json:"flush_idle_timeout_secs"`
	GoalVerifierCount               *int           `json:"goal_verifier_count"`
	GoalClassifierMaxRuns           *uint32        `json:"goal_classifier_max_runs"`
	GoalPlannerEnabled              *bool          `json:"goal_planner_enabled"`
	GoalPlannerModel                *GoalRoleModel `json:"goal_planner_model"`
	GoalSummaryEnabled              *bool          `json:"goal_summary_enabled"`
	GoalStrategistEvery             *uint32        `json:"goal_strategist_every"`
	GoalStrategistModel             *GoalRoleModel `json:"goal_strategist_model"`
	GoalSkepticModels               goalRoleModels `json:"goal_skeptic_models"`
	WebFetchProxy                   *string        `json:"web_fetch_proxy"`
	WebFetchAllowedDomains          []string       `json:"web_fetch_allowed_domains"`
	CursorSkills                    *bool          `json:"cursor_skills_enabled"`
	CursorRules                     *bool          `json:"cursor_rules_enabled"`
	CursorAgents                    *bool          `json:"cursor_agents_enabled"`
	CursorMCPs                      *bool          `json:"cursor_mcps_enabled"`
	CursorHooks                     *bool          `json:"cursor_hooks_enabled"`
	ClaudeSkills                    *bool          `json:"claude_skills_enabled"`
	ClaudeRules                     *bool          `json:"claude_rules_enabled"`
	ClaudeAgents                    *bool          `json:"claude_agents_enabled"`
	ClaudeMCPs                      *bool          `json:"claude_mcps_enabled"`
	ClaudeHooks                     *bool          `json:"claude_hooks_enabled"`
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
	if !c.twoPassCompactionConfigured && remote.TwoPassCompactionEnabled != nil {
		c.TwoPassCompaction = *remote.TwoPassCompactionEnabled
	}
	if !c.memoryConfigured && remote.MemoryEnabled != nil {
		c.Memory.Enabled = *remote.MemoryEnabled
	}
	if !c.memoryInjectionConfigured && remote.MemoryInitialInjectionEnabled != nil {
		c.Memory.InitialInjection = *remote.MemoryInitialInjectionEnabled
	}
	if !c.memorySearchConfigured {
		if remote.MemorySearchMaxResults != nil {
			c.Memory.Search.MaxResults = max(1, *remote.MemorySearchMaxResults)
		}
		if remote.MemorySearchMinScore != nil {
			c.Memory.Search.MinScore = min(1, max(0, *remote.MemorySearchMinScore))
		}
		if remote.MemoryTemporalDecayEnabled != nil {
			c.Memory.Search.TemporalDecay.Enabled = *remote.MemoryTemporalDecayEnabled
		}
		if remote.MemoryTemporalDecayHalfLifeDays != nil {
			c.Memory.Search.TemporalDecay.HalfLifeDays = *remote.MemoryTemporalDecayHalfLifeDays
		}
		if remote.MemoryMMREnabled != nil {
			c.Memory.Search.MMR.Enabled = *remote.MemoryMMREnabled
		}
		if remote.MemoryMMRLambda != nil {
			c.Memory.Search.MMR.Lambda = min(1, max(0, *remote.MemoryMMRLambda))
		}
	}
	if !c.memoryDreamConfigured {
		if remote.DreamEnabled != nil {
			c.Memory.Dream.Enabled = *remote.DreamEnabled
		}
		if remote.DreamMinHours != nil {
			c.Memory.Dream.MinHours = *remote.DreamMinHours
		}
		if remote.DreamMinSessions != nil {
			c.Memory.Dream.MinSessions = *remote.DreamMinSessions
		}
		if remote.DreamCheckIntervalSeconds != nil {
			value := *remote.DreamCheckIntervalSeconds
			c.Memory.Dream.CheckIntervalSeconds = &value
		}
	}
	if !c.memoryFlushConfigured {
		if remote.FlushEnabled != nil {
			c.Memory.Flush.Enabled = *remote.FlushEnabled
		}
		if remote.FlushSoftThresholdTokens != nil {
			c.Memory.Flush.SoftThresholdTokens = max(0, *remote.FlushSoftThresholdTokens)
		}
		if remote.FlushIdleTimeoutSeconds != nil {
			value := *remote.FlushIdleTimeoutSeconds
			c.Memory.Flush.IdleTimeoutSeconds = &value
		}
	}
	if !c.goalVerifierConfigured && remote.GoalVerifierCount != nil {
		c.Goal.VerifierCount = normalizedGoalVerifierCount(*remote.GoalVerifierCount)
	}
	if !c.goalClassifierMaxConfigured && remote.GoalClassifierMaxRuns != nil {
		c.Goal.ClassifierMaxRuns = max(uint32(1), *remote.GoalClassifierMaxRuns)
	}
	if !c.goalPlannerConfigured && remote.GoalPlannerEnabled != nil {
		c.Goal.PlannerEnabled = *remote.GoalPlannerEnabled
		c.goalPlannerResolved = true
	}
	if !c.goalSummaryConfigured && remote.GoalSummaryEnabled != nil {
		c.Goal.SummaryEnabled = *remote.GoalSummaryEnabled
		c.goalSummaryResolved = true
	}
	if !c.goalStrategistEveryConfigured && remote.GoalStrategistEvery != nil {
		c.Goal.StrategistEvery = max(uint32(1), *remote.GoalStrategistEvery)
	}
	if c.Goal.StrategistModel == nil && remote.GoalStrategistModel != nil && remote.GoalStrategistModel.valid() {
		model := *remote.GoalStrategistModel
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		c.Goal.StrategistModel = &model
	}
	if c.Goal.PlannerModel == nil && remote.GoalPlannerModel != nil && remote.GoalPlannerModel.valid() {
		model := *remote.GoalPlannerModel
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		c.Goal.PlannerModel = &model
	}
	if len(c.Goal.SkepticModels) == 0 {
		c.Goal.SkepticModels = normalizeGoalRoleModels(remote.GoalSkepticModels)
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

type goalRoleModels []GoalRoleModel

func (m *goalRoleModels) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	for _, item := range raw {
		var model GoalRoleModel
		if json.Unmarshal(item, &model) == nil && model.valid() {
			*m = append(*m, model)
		}
	}
	return nil
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
