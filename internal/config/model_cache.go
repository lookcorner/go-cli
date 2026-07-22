package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

const (
	modelCacheTTL      = 5 * time.Minute
	maxModelCacheBytes = 8 << 20
	defaultContextSize = 256_000
)

var modelCacheMu sync.Mutex

type ModelCache struct {
	profiles map[string]ModelProfile
}

type modelCacheFile struct {
	FetchedAt  time.Time                  `json:"fetched_at"`
	Version    string                     `json:"grok_version"`
	AuthMethod string                     `json:"auth_method"`
	Origin     string                     `json:"origin"`
	ETag       string                     `json:"etag,omitempty"`
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

type ModelFetchRequest struct {
	AuthMethod       string
	Origin           string
	InferenceBaseURL string
	Token            string
	TokenHeader      string
	UserID           string
	Email            string
	HTTP             *http.Client
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
	profiles, ok := modelCacheProfiles(cached, authMethod)
	return ModelCache{profiles: profiles}, ok
}

func modelCacheProfiles(cached modelCacheFile, authMethod string) (map[string]ModelProfile, bool) {
	profiles := make(map[string]ModelProfile, len(cached.Models))
	for id, entry := range cached.Models {
		id = strings.TrimSpace(id)
		if id == "" || strings.TrimSpace(entry.Info.Model) == "" || strings.TrimSpace(entry.Info.BaseURL) == "" || entry.Info.ContextWindow <= 0 {
			return nil, false
		}
		backend := entry.Info.Backend
		switch backend {
		case "", "chat_completions":
			backend = "chat_completions"
		case "responses":
		case "messages":
			backend = "anthropic_messages"
		default:
			return nil, false
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
			return nil, false
		}
		profiles[id] = profile
	}
	return profiles, true
}

// FetchModelCache refreshes the OpenAI-compatible model catalog and atomically
// replaces models_cache.json only after at least one valid model is parsed.
func FetchModelCache(ctx context.Context, request ModelFetchRequest) (ModelCache, error) {
	if request.AuthMethod != "api_key" && request.AuthMethod != "session" && request.AuthMethod != "deployment" {
		return ModelCache{}, fmt.Errorf("unsupported model auth method %q", request.AuthMethod)
	}
	if strings.TrimSpace(request.Origin) == "" || strings.TrimSpace(request.Token) == "" {
		return ModelCache{}, errors.New("model catalog origin and token are required")
	}
	client := request.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Origin, nil)
	if err != nil {
		return ModelCache{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.Token)
	httpRequest.Header.Set("x-grok-client-version", version.Current)
	if request.AuthMethod == "session" {
		header := request.TokenHeader
		if header == "" {
			header = "xai-grok-cli"
		}
		httpRequest.Header.Set("X-XAI-Token-Auth", header)
		httpRequest.Header.Set("x-userid", request.UserID)
		if request.Email != "" {
			httpRequest.Header.Set("x-email", request.Email)
		}
		httpRequest.Header.Set("x-grok-client-identifier", "gork-go")
		httpRequest.Header.Set("x-grok-client-mode", "interactive")
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return ModelCache{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return ModelCache{}, fmt.Errorf("fetch models failed (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxModelCacheBytes+1))
	if err != nil {
		return ModelCache{}, err
	}
	if len(data) > maxModelCacheBytes {
		return ModelCache{}, errors.New("models response exceeds 8 MiB")
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ModelCache{}, fmt.Errorf("decode models response: %w", err)
	}
	entries := make(map[string]modelCacheEntry, len(payload.Data))
	for _, encoded := range payload.Data {
		var raw map[string]any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil || raw == nil {
			continue
		}
		id, entry, ok := parseRemoteModel(raw, request.InferenceBaseURL, request.AuthMethod)
		if ok {
			entries[id] = entry
		}
	}
	if len(entries) == 0 {
		return ModelCache{}, errors.New("models endpoint returned no valid models")
	}
	cached := modelCacheFile{
		FetchedAt: time.Now().UTC(), Version: version.Current, AuthMethod: request.AuthMethod,
		Origin: request.Origin, ETag: response.Header.Get("ETag"), Models: entries,
	}
	profiles, ok := modelCacheProfiles(cached, request.AuthMethod)
	if !ok {
		return ModelCache{}, errors.New("models response produced an invalid cache")
	}
	if err := writeModelCacheFile(cached); err != nil {
		return ModelCache{}, err
	}
	return ModelCache{profiles: profiles}, nil
}

func parseRemoteModel(raw map[string]any, defaultBaseURL, authMethod string) (string, modelCacheEntry, bool) {
	meta, _ := raw["_meta"].(map[string]any)
	model := firstModelString(raw, "model", "modelId", "id")
	if model == "" {
		model = firstModelString(meta, "model", "modelId")
	}
	if model == "" {
		return "", modelCacheEntry{}, false
	}
	id := firstModelString(raw, "id")
	if id == "" {
		id = model
	}
	baseURL := firstModelString(raw, "baseUrl", "base_url")
	if baseURL == "" {
		baseURL = strings.TrimRight(defaultBaseURL, "/")
	}
	if baseURL == "" {
		return "", modelCacheEntry{}, false
	}
	contextWindow, present := firstModelInt(raw, "contextWindow", "context_window")
	if !present {
		contextWindow, present = firstModelInt(meta, "contextWindow", "totalContextTokens")
	}
	if !present {
		contextWindow = defaultContextSize
	}
	if contextWindow <= 0 {
		return "", modelCacheEntry{}, false
	}
	backend := firstModelString(raw, "apiBackend", "api_backend")
	if backend == "" {
		backend = "chat_completions"
	}
	if backend != "responses" && backend != "chat_completions" && backend != "messages" {
		backend = "chat_completions"
	}
	reasoningEffort := firstModelString(raw, "reasoningEffort", "reasoning_effort")
	if reasoningEffort == "" {
		reasoningEffort = firstModelString(meta, "reasoningEffort")
	}
	reasoningEffort = normalizeReasoningEffort(reasoningEffort)
	if reasoningEffort == "invalid" {
		reasoningEffort = ""
	}
	reasoningEfforts := firstModelSlice(raw, "reasoningEfforts", "reasoning_efforts")
	if reasoningEfforts == nil {
		reasoningEfforts = firstModelSlice(meta, "reasoningEfforts")
	}
	options := parseRemoteReasoningEffortOptions(reasoningEfforts)
	info := modelCacheInfo{
		Model: model, BaseURL: baseURL, Name: firstModelString(raw, "name"),
		Description: firstModelString(raw, "description"), Backend: backend, ContextWindow: contextWindow,
		Hidden: firstModelBool(raw, meta, "hidden"), SupportedInAPI: firstModelBoolPointer(raw, meta, "supportedInApi", "supported_in_api"),
		ReasoningEffort: reasoningEffort, SupportsReasoningEffort: firstModelBool(raw, meta, "supportsReasoningEffort", "supports_reasoning_effort"),
		ReasoningEfforts: options,
	}
	if info.Name == "" {
		info.Name = model
	}
	if value, ok := firstModelInt(raw, "autoCompactThresholdPercent", "auto_compact_threshold_percent"); ok && value >= 0 && value <= 100 {
		info.AutoCompactThresholdPercent = &value
	}
	entry := modelCacheEntry{Info: info, APIBaseURL: firstModelString(raw, "apiBaseUrl", "api_base_url")}
	if authMethod == "api_key" && entry.APIBaseURL == "" {
		entry.APIBaseURL = strings.TrimRight(defaultBaseURL, "/")
	}
	return id, entry, true
}

func firstModelString(values map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := values[name].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstModelInt(values map[string]any, names ...string) (int, bool) {
	for _, name := range names {
		switch value := values[name].(type) {
		case float64:
			limit := math.Ldexp(1, strconv.IntSize-1)
			if !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value && value >= -limit && value < limit {
				return int(value), true
			}
		case json.Number:
			parsed, err := strconv.ParseInt(value.String(), 10, 0)
			if err == nil {
				return int(parsed), true
			}
		}
	}
	return 0, false
}

func parseRemoteReasoningEffortOptions(raw []any) []ReasoningEffortOption {
	options := make([]ReasoningEffortOption, 0, len(raw))
	for _, item := range raw {
		parsed, err := parseReasoningEffortOptions([]any{item})
		if err != nil || len(parsed) == 0 {
			continue
		}
		parsed[0].Value = normalizeReasoningEffort(parsed[0].Value)
		if parsed[0].Value == "" || parsed[0].Value == "invalid" {
			continue
		}
		options = append(options, parsed[0])
	}
	return options
}

func firstModelSlice(values map[string]any, names ...string) []any {
	for _, name := range names {
		if value, ok := values[name].([]any); ok {
			return value
		}
	}
	return nil
}

func firstModelBool(values, meta map[string]any, names ...string) bool {
	if value := firstModelBoolPointer(values, meta, names...); value != nil {
		return *value
	}
	return false
}

func firstModelBoolPointer(values, meta map[string]any, names ...string) *bool {
	for _, source := range []map[string]any{values, meta} {
		for _, name := range names {
			if value, ok := source[name].(bool); ok {
				return &value
			}
		}
	}
	return nil
}

func writeModelCacheFile(cache modelCacheFile) error {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	path, err := ModelCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".models-*.json")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func ClearModelCache() error {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	path, err := ModelCachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
