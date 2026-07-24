package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

func TestModelsCLIUsesConfiguredCatalogAndFilters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("GROK_CODE_XAI_API_KEY", "")
	path := filepath.Join(home, "config.toml")
	content := `
[models]
default = "smart"
allowed_models = ["smart", "fast", "hidden"]
hidden_models = ["hidden"]

[model.smart]
model = "smart-api"
api_key = "model-key"

[model.fast]
model = "fast-api"

[model.hidden]
model = "hidden-api"

[model.disabled]
model = "disabled-api"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"--config", path}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, expected := range []string{
		`Model "smart" is using its own API key.`,
		"Default model: smart",
		"* smart (default)",
		"- fast",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "disabled") {
		t.Fatalf("filtered models were listed:\n%s", text)
	}
	if stderr.Len() != 0 {
		t.Fatalf("model credentials triggered a catalog refresh:\n%s", stderr.String())
	}
}

func TestModelsAuthStatusPriority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	authPath := filepath.Join(home, "auth.json")
	authConfig := auth.DefaultConfig()
	cfg := config.Config{
		DeploymentKey: "deployment",
		ModelProfiles: map[string]config.ModelProfile{
			"z-model": {APIKey: "second"},
			"a-model": {APIKey: "first"},
		},
	}
	t.Setenv("XAI_API_KEY", "environment")
	if got := modelsAuthStatus(cfg, authPath, authConfig); got != "You are using XAI_API_KEY." {
		t.Fatalf("environment status=%q", got)
	}
	t.Setenv("XAI_API_KEY", "")
	if err := auth.Save(authPath, authConfig.Scope(), auth.Credential{Key: "session"}); err != nil {
		t.Fatal(err)
	}
	if got := modelsAuthStatus(cfg, authPath, authConfig); !strings.Contains(got, "logged in with") {
		t.Fatalf("session status=%q", got)
	}
	if err := auth.Remove(authPath, authConfig.Scope()); err != nil {
		t.Fatal(err)
	}
	if got := modelsAuthStatus(cfg, authPath, authConfig); got != `Model "a-model" is using its own API key.` {
		t.Fatalf("model status=%q", got)
	}
	cfg.DisableAPIKeyAuth = true
	if got := modelsAuthStatus(cfg, authPath, authConfig); got != "You are authenticated via deployment key." {
		t.Fatalf("deployment status=%q", got)
	}
	cfg.DeploymentKey = ""
	if got := modelsAuthStatus(cfg, authPath, authConfig); got != "You are not authenticated." {
		t.Fatalf("anonymous status=%q", got)
	}
}

func TestPrintModelsUsesVisibleDefaultFallback(t *testing.T) {
	cfg := config.Config{
		Model:          "hidden-api",
		DefaultModelID: "hidden",
		ModelProfiles: map[string]config.ModelProfile{
			"hidden": {Model: "hidden-api", Hidden: true},
			"fast":   {Model: "fast-api"},
			"gone":   {Model: "gone-api"},
		},
		DisabledModels: []string{"gone"},
	}
	var output bytes.Buffer
	printModels(&output, cfg, "You are not authenticated.")
	text := output.String()
	if !strings.Contains(text, "Default model: fast") || !strings.Contains(text, "* fast (default)") || strings.Contains(text, "hidden") || strings.Contains(text, "gone") {
		t.Fatalf("unexpected model output:\n%s", text)
	}
}

func TestModelsCLIRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"extra"}, &stdout, &stderr); err == nil {
		t.Fatal("unexpected argument was accepted")
	}
}
