package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

const (
	modelCacheTTL      = 5 * time.Minute
	maxModelCacheBytes = 8 << 20
)

type ModelCache struct {
	profiles map[string]ModelProfile
}

type modelCacheFile struct {
	FetchedAt  time.Time                  `json:"fetched_at"`
	Version    string                     `json:"grok_version"`
	AuthMethod string                     `json:"auth_method"`
	Origin     string                     `json:"origin"`
	Models     map[string]modelCacheEntry `json:"models"`
}

type modelCacheEntry struct {
	Info       modelCacheInfo `json:"info"`
	APIBaseURL string         `json:"api_base_url"`
}

type modelCacheInfo struct {
	Model                       string                  `json:"model"`
	BaseURL                     string                  `json:"base_url"`
	Name                        string                  `json:"name"`
	Description                 string                  `json:"description"`
	Backend                     string                  `json:"api_backend"`
	ContextWindow               int                     `json:"context_window"`
	AutoCompactThresholdPercent *int                    `json:"auto_compact_threshold_percent"`
	Hidden                      bool                    `json:"hidden"`
	SupportedInAPI              *bool                   `json:"supported_in_api"`
	ReasoningEffort             string                  `json:"reasoning_effort"`
	SupportsReasoningEffort     bool                    `json:"supports_reasoning_effort"`
	ReasoningEfforts            []ReasoningEffortOption `json:"reasoning_efforts"`
}

func ModelCachePath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, "models_cache.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok", "models_cache.json"), nil
}

func LoadModelCache(authMethod, origin string) (ModelCache, bool) {
	path, err := ModelCachePath()
	if err != nil {
		return ModelCache{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return ModelCache{}, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxModelCacheBytes+1))
	if err != nil || len(data) > maxModelCacheBytes {
		return ModelCache{}, false
	}
	var cached modelCacheFile
	if json.Unmarshal(data, &cached) != nil || cached.Version != version.Current || cached.AuthMethod != authMethod || cached.Origin != origin || cached.Models == nil {
		return ModelCache{}, false
	}
	age := time.Since(cached.FetchedAt)
	if age < 0 || age >= modelCacheTTL {
		return ModelCache{}, false
	}
	profiles := make(map[string]ModelProfile, len(cached.Models))
	for id, entry := range cached.Models {
		id = strings.TrimSpace(id)
		if id == "" || strings.TrimSpace(entry.Info.Model) == "" || strings.TrimSpace(entry.Info.BaseURL) == "" || entry.Info.ContextWindow <= 0 {
			return ModelCache{}, false
		}
		backend := entry.Info.Backend
		switch backend {
		case "", "chat_completions":
			backend = "chat_completions"
		case "responses":
		case "messages":
			backend = "anthropic_messages"
		default:
			return ModelCache{}, false
		}
		baseURL := entry.Info.BaseURL
		if authMethod == "api_key" && strings.TrimSpace(entry.APIBaseURL) != "" {
			baseURL = entry.APIBaseURL
		}
		hidden := entry.Info.Hidden || authMethod == "api_key" && entry.Info.SupportedInAPI != nil && !*entry.Info.SupportedInAPI
		profile, err := normalizeModelProfile(id, ModelProfile{
			Model: entry.Info.Model, Name: entry.Info.Name, Description: entry.Info.Description,
			BaseURL: baseURL, Backend: backend, ContextWindow: entry.Info.ContextWindow,
			AutoCompactThresholdPercent: entry.Info.AutoCompactThresholdPercent, Hidden: hidden,
			ReasoningEffort: entry.Info.ReasoningEffort, SupportsReasoningEffort: entry.Info.SupportsReasoningEffort,
			ReasoningEfforts: append([]ReasoningEffortOption(nil), entry.Info.ReasoningEfforts...),
		})
		if err != nil {
			return ModelCache{}, false
		}
		profiles[id] = profile
	}
	return ModelCache{profiles: profiles}, true
}

func (c *Config) ApplyModelCache(cache ModelCache) {
	if cache.profiles == nil {
		return
	}
	configured := c.ModelProfiles
	c.ModelProfiles = cloneModelProfiles(cache.profiles)
	for id, override := range configured {
		profile := c.ModelProfiles[id]
		mergeModelProfile(&profile, override)
		c.ModelProfiles[id] = profile
	}
}

func mergeModelProfile(target *ModelProfile, source ModelProfile) {
	if source.Model != "" {
		target.Model = source.Model
	}
	if source.Name != "" {
		target.Name = source.Name
	}
	if source.Description != "" {
		target.Description = source.Description
	}
	if source.hiddenConfigured || source.Hidden {
		target.Hidden = source.Hidden
	}
	if source.BaseURL != "" {
		target.BaseURL = source.BaseURL
	}
	if source.APIKey != "" {
		target.APIKey = source.APIKey
	}
	if source.Backend != "" {
		target.Backend = source.Backend
	}
	if source.ContextWindow > 0 {
		target.ContextWindow = source.ContextWindow
	}
	if source.AutoCompactThresholdPercent != nil {
		value := *source.AutoCompactThresholdPercent
		target.AutoCompactThresholdPercent = &value
	}
	if source.ReasoningEffort != "" {
		target.ReasoningEffort = source.ReasoningEffort
	}
	if source.supportsReasoningConfigured || source.SupportsReasoningEffort {
		target.SupportsReasoningEffort = source.SupportsReasoningEffort
	}
	if source.ReasoningEfforts != nil {
		target.ReasoningEfforts = append([]ReasoningEffortOption(nil), source.ReasoningEfforts...)
	}
}
