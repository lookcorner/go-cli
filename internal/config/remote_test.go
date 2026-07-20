package config

import (
	"context"
	"encoding/json"
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

func TestFetchRemoteSettingsRetriesAndAuthenticates(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.URL.Path != "/v1/settings" || request.Header.Get("Authorization") != "Bearer session-token" {
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
