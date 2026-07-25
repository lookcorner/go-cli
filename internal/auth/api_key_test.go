package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAPIKeyEnvironment(t *testing.T) {
	t.Setenv("XAI_API_KEY", "current")
	t.Setenv("GROK_CODE_XAI_API_KEY", "legacy")
	if key, ok := ReadAPIKeyEnvironment(); !ok || key != "current" {
		t.Fatalf("current key=%q present=%v", key, ok)
	}

	t.Setenv("XAI_API_KEY", "")
	if key, ok := ReadAPIKeyEnvironment(); !ok || key != "" {
		t.Fatalf("empty current key=%q present=%v", key, ok)
	}

	if err := os.Unsetenv("XAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if key, ok := ReadAPIKeyEnvironment(); !ok || key != "legacy" {
		t.Fatalf("legacy key=%q present=%v", key, ok)
	}

	if err := os.Unsetenv("GROK_CODE_XAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if key, ok := ReadAPIKeyEnvironment(); ok || key != "" {
		t.Fatalf("missing key=%q present=%v", key, ok)
	}
}

func TestStoreAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "other", Credential{Key: "token", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	if err := StoreAPIKey(path, "secret"); err != nil {
		t.Fatal(err)
	}
	credential, err := Load(path, APIKeyScope)
	if err != nil || credential.Key != "secret" || credential.AuthMode != "api_key" || credential.CreateTime.IsZero() {
		t.Fatalf("API key credential=%#v err=%v", credential, err)
	}
	if other, err := Load(path, "other"); err != nil || other.Key != "token" {
		t.Fatalf("other credential=%#v err=%v", other, err)
	}

	if err := StoreAPIKey(path, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, APIKeyScope); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed API key error=%v", err)
	}
	if other, err := Load(path, "other"); err != nil || other.Key != "token" {
		t.Fatalf("preserved credential=%#v err=%v", other, err)
	}
}

func TestResolveAPIKeyFallsBackFromEnvironmentToStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := StoreAPIKey(path, " stored-key "); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAI_API_KEY", " environment-key ")
	if key, err := ResolveAPIKey(path); err != nil || key != "environment-key" {
		t.Fatalf("environment key=%q err=%v", key, err)
	}
	t.Setenv("XAI_API_KEY", "")
	if key, err := ResolveAPIKey(path); err != nil || key != "stored-key" {
		t.Fatalf("stored key=%q err=%v", key, err)
	}
}
