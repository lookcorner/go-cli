package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }
func uint32Pointer(value uint32) *uint32 { return &value }
func uint64Pointer(value uint64) *uint64 { return &value }

func TestFetchRemoteSettingsRetriesAndAuthenticates(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.URL.Path != "/v1/settings" || request.Header.Get("Authorization") != "Bearer session-token" || request.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" || request.Header.Get("x-grok-client-version") == "" || request.Header.Get("x-grok-client-identifier") != "gork-go" || request.Header.Get("x-grok-client-mode") != "interactive" {
			t.Fatalf("request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		if attempts == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(writer).Encode(RemoteSettings{WebFetchEnabled: boolPointer(true), GoalVerifierCount: intPointer(4), GoalClassifierMaxRuns: uint32Pointer(7)})
	}))
	defer server.Close()
	settings := FetchRemoteSettings(context.Background(), server.URL+"/v1", "session-token", server.Client())
	if settings == nil || settings.WebFetchEnabled == nil || !*settings.WebFetchEnabled || settings.GoalVerifierCount == nil || *settings.GoalVerifierCount != 4 || settings.GoalClassifierMaxRuns == nil || *settings.GoalClassifierMaxRuns != 7 || attempts != 2 {
		t.Fatalf("settings=%#v attempts=%d", settings, attempts)
	}
}

func TestFetchRemoteSettingsIncludesSessionIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-email") != "user@example.com" {
			t.Fatalf("identity headers=%#v", request.Header)
		}
		fmt.Fprint(writer, `{}`)
	}))
	defer server.Close()
	if settings := FetchRemoteSettingsForSession(context.Background(), server.URL, "token", "user-1", "user@example.com", server.Client()); settings == nil {
		t.Fatal("settings fetch failed")
	}
}

func TestRemoteSettingsDecodesACPClientFields(t *testing.T) {
	var settings RemoteSettings
	if err := json.Unmarshal([]byte(`{"sharing_enabled":true,"session_picker_grouped":false,"tips":["one"],"announcements":[{"id":"notice"}],"permission_mode":"auto","group_tool_verbs":true,"collapsed_edit_blocks":false,"subscription_watch_interval_secs":30}`), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.SharingEnabled == nil || !*settings.SharingEnabled || settings.SessionPickerGrouped == nil || *settings.SessionPickerGrouped || len(settings.Tips) != 1 || len(settings.Announcements) != 1 || settings.PermissionMode == nil || *settings.PermissionMode != "auto" || settings.GroupToolVerbs == nil || !*settings.GroupToolVerbs || settings.CollapsedEditBlocks == nil || *settings.CollapsedEditBlocks || settings.SubscriptionWatchIntervalSeconds == nil || *settings.SubscriptionWatchIntervalSeconds != 30 {
		t.Fatalf("settings=%#v", settings)
	}
}

func TestBillingRemoteMetadataRefreshesAndClears(t *testing.T) {
	var remote RemoteSettings
	if err := json.Unmarshal([]byte(`{"subscription_tier":"supergrok","subscription_tier_display":"SuperGrok","on_demand_enabled":true}`), &remote); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	cfg.ApplyRemoteSettings(&remote)
	if cfg.SubscriptionTier == nil || *cfg.SubscriptionTier != "supergrok" || cfg.SubscriptionTierDisplay == nil || *cfg.SubscriptionTierDisplay != "SuperGrok" || cfg.OnDemandEnabled == nil || !*cfg.OnDemandEnabled {
		t.Fatalf("billing metadata=%#v", cfg)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if cfg.SubscriptionTier != nil || cfg.SubscriptionTierDisplay != nil || cfg.OnDemandEnabled != nil {
		t.Fatalf("stale billing metadata survived refresh: %#v", cfg)
	}
}

func TestAccessGateRemoteMetadataRefreshesAndClears(t *testing.T) {
	var remote RemoteSettings
	if err := json.Unmarshal([]byte(`{"allow_access":true,"gate_message":"Upgrade","gate_url":"https://example.com/upgrade","gate_label":"Subscribe","show_resolved_model":false}`), &remote); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	cfg.ApplyRemoteSettings(&remote)
	if cfg.AllowAccess == nil || !*cfg.AllowAccess || cfg.GateMessage == nil || *cfg.GateMessage != "Upgrade" || cfg.GateURL == nil || cfg.GateLabel == nil || cfg.ShowResolvedModel == nil || *cfg.ShowResolvedModel {
		t.Fatalf("gate metadata=%#v", cfg)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if cfg.AllowAccess != nil || cfg.GateMessage != nil || cfg.GateURL != nil || cfg.GateLabel != nil || cfg.ShowResolvedModel != nil {
		t.Fatalf("stale gate metadata survived refresh: %#v", cfg)
	}
}

func TestGoalClassifierMaxRunsRemoteAndLocalPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.Goal.ClassifierMaxRuns != 10 {
		t.Fatalf("default=%#v err=%v", cfg.Goal, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalClassifierMaxRuns: uint32Pointer(0)})
	if cfg.Goal.ClassifierMaxRuns != 1 {
		t.Fatalf("remote floor=%#v", cfg.Goal)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nclassifier_max_runs = 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalClassifierMaxRuns: uint32Pointer(8)})
	if cfg.Goal.ClassifierMaxRuns != 6 {
		t.Fatalf("local precedence=%#v", cfg.Goal)
	}
}

func TestGoalRoleModelsRemoteToleranceAndPrecedence(t *testing.T) {
	var remote RemoteSettings
	data := []byte(`{
		"goal_strategist_every": 4,
		"goal_strategist_model": {"model":"strategy","agent_type":"cursor"},
		"goal_skeptic_models": [
			{"model":"first","agent_type":"general-purpose"},
			{"model":"broken"},
			42,
			{"model":"second","agent_type":"plan"}
		]
	}`)
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&remote)
	if cfg.GoalStrategistEvery() != 4 || cfg.Goal.StrategistModel == nil || cfg.Goal.StrategistModel.Model != "strategy" || len(cfg.Goal.SkepticModels) != 2 || cfg.Goal.SkepticModels[1].Model != "second" {
		t.Fatalf("remote goal config=%#v", cfg.Goal)
	}

	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	local := `[goal]
strategist_every = 2
strategist_model = { model = "local", agent_type = "general-purpose" }
skeptic_models = [{ model = "local-skeptic", agent_type = "general-purpose" }]
`
	if err := os.WriteFile(path, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&remote)
	if cfg.GoalStrategistEvery() != 2 || cfg.Goal.StrategistModel.Model != "local" || len(cfg.Goal.SkepticModels) != 1 || cfg.Goal.SkepticModels[0].Model != "local-skeptic" {
		t.Fatalf("local precedence=%#v", cfg.Goal)
	}
}

func TestGoalRoleModelsMalformedRemoteFieldsDoNotFailSettings(t *testing.T) {
	for _, data := range []string{
		`{"goal_strategist_model":"bad","goal_skeptic_models":{},"web_fetch_enabled":true}`,
		`{"goal_strategist_model":{"model":1,"agent_type":[]},"goal_skeptic_models":"bad","web_fetch_enabled":true}`,
	} {
		var settings RemoteSettings
		if err := json.Unmarshal([]byte(data), &settings); err != nil || settings.WebFetchEnabled == nil || !*settings.WebFetchEnabled {
			t.Fatalf("settings=%#v err=%v", settings, err)
		}
		cfg := Config{}
		cfg.ApplyRemoteSettings(&settings)
		if cfg.Goal.StrategistModel != nil || len(cfg.Goal.SkepticModels) != 0 {
			t.Fatalf("malformed roles survived: %#v", cfg.Goal)
		}
	}
}

func TestApplyRemoteSettingsUsesLocalAndEnvironmentPrecedence(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[features]\nweb_fetch = false\nauto_wake = false\n[toolset.web_fetch]\nproxy_endpoint = \"https://local.example\"\n[compat.cursor]\nskills = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER", "false")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		OfficialMarketplaceAutoRegister: boolPointer(true), WebFetchEnabled: boolPointer(true), AutoWakeEnabled: boolPointer(true),
		WebFetchProxy: stringPointer("https://remote.example"), WebFetchAllowedDomains: []string{"docs.example"},
		CursorSkills: boolPointer(false), ClaudeHooks: boolPointer(false),
	})
	if cfg.OfficialMarketplaceAutoRegister || cfg.WebFetch.Enabled || cfg.AutoWakeEnabled || cfg.WebFetch.ProxyEndpoint != "https://local.example" || !cfg.Compat.Cursor.Skills || cfg.Compat.Claude.Hooks || !cfg.WebFetch.DomainsConfigured || len(cfg.WebFetch.AllowedDomains) != 1 {
		t.Fatalf("config=%#v", cfg)
	}
}

func TestFeedbackRemoteRefreshAndLocalPrecedence(t *testing.T) {
	cfg := Config{FeedbackEnabled: true}
	cfg.ApplyRemoteSettings(&RemoteSettings{FeedbackEnabled: boolPointer(false)})
	if cfg.FeedbackEnabled {
		t.Fatalf("remote disable=%#v", cfg)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.FeedbackEnabled {
		t.Fatalf("omitted remote flag did not restore default: %#v", cfg)
	}

	cfg = Config{FeedbackEnabled: false, feedbackConfigured: true}
	cfg.ApplyRemoteSettings(&RemoteSettings{FeedbackEnabled: boolPointer(true)})
	if cfg.FeedbackEnabled {
		t.Fatalf("remote overrode local config: %#v", cfg)
	}
}

func TestAutoModeRemoteRefreshAndFieldPrecedence(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{AutoMode: &AutoModeConfig{
		Enabled: boolPointer(false), PromptType: "bare_instructions", ClassifierModel: "remote", ReasoningEffort: "high",
	}})
	if cfg.AutoModeEnabled() || cfg.AutoMode.PromptType != "bare_instructions" || cfg.AutoMode.ClassifierModel != "remote" || cfg.AutoMode.ReasoningEffort != "high" {
		t.Fatalf("first remote=%#v", cfg.AutoMode)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{AutoMode: &AutoModeConfig{PromptType: "just_command"}})
	if !cfg.AutoModeEnabled() || cfg.AutoMode.PromptType != "just_command" || cfg.AutoMode.ClassifierModel != "" || cfg.AutoMode.ReasoningEffort != "" {
		t.Fatalf("refreshed remote=%#v", cfg.AutoMode)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{WebFetchEnabled: boolPointer(true)})
	if !cfg.AutoModeEnabled() || cfg.AutoMode.PromptType != "" {
		t.Fatalf("omitted remote auto mode survived=%#v", cfg.AutoMode)
	}

	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[auto_mode]\nprompt_type = \"full\"\nclassifier_model = \"local\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{AutoMode: &AutoModeConfig{
		Enabled: boolPointer(false), PromptType: "bare_instructions", ClassifierModel: "remote", ReasoningEffort: "low",
	}})
	if cfg.AutoModeEnabled() || cfg.AutoMode.PromptType != "full" || cfg.AutoMode.ClassifierModel != "local" || cfg.AutoMode.ReasoningEffort != "low" {
		t.Fatalf("local precedence=%#v", cfg.AutoMode)
	}
}

func TestMalformedRemoteAutoModeDoesNotPoisonOtherSettings(t *testing.T) {
	for _, data := range []string{
		`{"auto_mode":{"enabled":"yes"},"web_fetch_enabled":true}`,
		`{"auto_mode":{"prompt_type":"typo"},"web_fetch_enabled":true}`,
		`{"auto_mode":42,"web_fetch_enabled":true}`,
	} {
		var settings RemoteSettings
		if err := json.Unmarshal([]byte(data), &settings); err != nil || settings.AutoMode != nil || settings.WebFetchEnabled == nil || !*settings.WebFetchEnabled {
			t.Fatalf("settings=%#v err=%v", settings, err)
		}
	}
}

func TestAutoWakeDefaultsOnAndRemoteCanDisable(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoWakeEnabled {
		t.Fatal("auto wake should default on")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{AutoWakeEnabled: boolPointer(false)})
	if cfg.AutoWakeEnabled {
		t.Fatal("remote auto wake gate was not applied")
	}
}

func TestTwoPassCompactionPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.TwoPassCompaction {
		t.Fatalf("default=%v err=%v", cfg.TwoPassCompaction, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{TwoPassCompactionEnabled: boolPointer(true)})
	if !cfg.TwoPassCompaction {
		t.Fatal("remote feature flag was not applied")
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[features]\ntwo_pass_compaction = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{TwoPassCompactionEnabled: boolPointer(true)})
	if cfg.TwoPassCompaction {
		t.Fatal("remote setting overrode local feature config")
	}
	t.Setenv("GROK_TWO_PASS_COMPACTION", "false")
	if err := os.WriteFile(path, []byte("[features]\ntwo_pass_compaction = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{TwoPassCompactionEnabled: boolPointer(true)})
	if cfg.TwoPassCompaction {
		t.Fatal("environment kill switch did not win")
	}
}

func TestMemoryDefaultsRemoteAndEnvironmentPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.Memory.Enabled || !cfg.Memory.InitialInjection || !cfg.Memory.SaveOnEnd || !cfg.Memory.Flush.Enabled || cfg.Memory.Flush.SoftThresholdTokens != 4000 || cfg.Memory.Flush.MaxWriteChars != 8000 || cfg.Memory.Flush.IdleTimeoutSeconds != nil || cfg.Memory.Search.RecencyDecay != 0.95 || !cfg.Memory.Search.TemporalDecay.Enabled || cfg.Memory.Search.TemporalDecay.HalfLifeDays != 7 || cfg.Memory.Search.MMR.Enabled || cfg.Memory.Search.MMR.Lambda != 0.7 || cfg.Memory.Search.SourceWeights["workspace"] != 1 || cfg.Memory.GC.MaxAgeDays != 30 {
		t.Fatalf("defaults=%#v err=%v", cfg.Memory, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		MemoryEnabled: boolPointer(true), MemoryInitialInjectionEnabled: boolPointer(false),
		FlushEnabled: boolPointer(false), FlushSoftThresholdTokens: intPointer(2000), FlushIdleTimeoutSeconds: uint64Pointer(120),
		MemorySearchMaxResults: intPointer(9), MemorySearchMinScore: float64Pointer(0.6),
		MemoryTemporalDecayEnabled: boolPointer(false), MemoryTemporalDecayHalfLifeDays: float64Pointer(14),
		MemoryMMREnabled: boolPointer(true), MemoryMMRLambda: float64Pointer(2),
		DreamEnabled: boolPointer(false), DreamMinHours: uint64Pointer(8), DreamMinSessions: uint64Pointer(6), DreamCheckIntervalSeconds: uint64Pointer(900),
	})
	if !cfg.Memory.Enabled || cfg.Memory.InitialInjection || cfg.Memory.Flush.Enabled || cfg.Memory.Flush.SoftThresholdTokens != 2000 || cfg.Memory.Flush.IdleTimeoutSeconds == nil || *cfg.Memory.Flush.IdleTimeoutSeconds != 120 || cfg.Memory.Search.MaxResults != 9 || cfg.Memory.Search.MinScore != 0.6 || cfg.Memory.Search.TemporalDecay.Enabled || cfg.Memory.Search.TemporalDecay.HalfLifeDays != 14 || !cfg.Memory.Search.MMR.Enabled || cfg.Memory.Search.MMR.Lambda != 1 || cfg.Memory.Dream.Enabled || cfg.Memory.Dream.MinHours != 8 || cfg.Memory.Dream.MinSessions != 6 || cfg.Memory.Dream.CheckIntervalSeconds == nil || *cfg.Memory.Dream.CheckIntervalSeconds != 900 {
		t.Fatalf("remote=%#v", cfg.Memory)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[memory]\n[compaction.memory_flush]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{MemoryEnabled: boolPointer(true), FlushEnabled: boolPointer(false), FlushIdleTimeoutSeconds: uint64Pointer(120)})
	if cfg.Memory.Enabled || !cfg.Memory.Flush.Enabled || cfg.Memory.Flush.IdleTimeoutSeconds != nil {
		t.Fatalf("empty local sections did not block remote values: %#v", cfg.Memory)
	}
	if err := os.WriteFile(path, []byte("[memory.search]\nmax_results = 8\n[memory.search.mmr]\nlambda = -0.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		MemorySearchMaxResults: intPointer(3), MemoryTemporalDecayEnabled: boolPointer(false),
		MemoryMMREnabled: boolPointer(true), MemoryMMRLambda: float64Pointer(0.4),
	})
	if cfg.Memory.Search.MaxResults != 8 || !cfg.Memory.Search.TemporalDecay.Enabled || cfg.Memory.Search.MMR.Enabled || cfg.Memory.Search.MMR.Lambda != 0 {
		t.Fatalf("local search did not block remote values: %#v", cfg.Memory.Search)
	}
	t.Setenv("GROK_MEMORY", "false")
	if err := os.WriteFile(path, []byte("[memory]\nenabled = true\n[compaction.memory_flush]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{MemoryEnabled: boolPointer(true), FlushEnabled: boolPointer(false), FlushIdleTimeoutSeconds: uint64Pointer(120)})
	if cfg.Memory.Enabled || !cfg.Memory.Flush.Enabled || cfg.Memory.Flush.IdleTimeoutSeconds != nil {
		t.Fatalf("local/env precedence=%#v", cfg.Memory)
	}
	cfg.OverrideMemory(true)
	if !cfg.Memory.Enabled {
		t.Fatal("CLI memory override was not applied")
	}
}

func float64Pointer(value float64) *float64 { return &value }

func TestGoalVerifierCountRemoteAndLocalPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.Goal.VerifierCount != 3 {
		t.Fatalf("default=%#v err=%v", cfg.Goal, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalVerifierCount: intPointer(99)})
	if cfg.Goal.VerifierCount != 5 {
		t.Fatalf("remote clamp=%#v", cfg.Goal)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[goal]\nverifier_count = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalVerifierCount: intPointer(2)})
	if cfg.Goal.VerifierCount != 4 {
		t.Fatalf("local precedence=%#v", cfg.Goal)
	}
	t.Setenv("GROK_GOAL_VERIFIER_N", "1")
	cfg, err = Load(filepath.Join(home, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{GoalVerifierCount: intPointer(5)})
	if cfg.Goal.VerifierCount != 1 {
		t.Fatalf("environment precedence=%#v", cfg.Goal)
	}
}
