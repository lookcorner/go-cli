package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLogoutCurrentScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "current", Credential{Key: "current-token", Email: "user@example.com", TeamID: "team-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "sibling", Credential{Key: "sibling-token"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAI_API_KEY", "")

	result, err := Logout(path, "current", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WasLoggedIn || result.Email != "user@example.com" || !result.APIKeyStillSet || !result.ClearedCurrent || result.Credential.TeamID != "team-1" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := Load(path, "current"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current scope still loads: %v", err)
	}
	if sibling, err := Load(path, "sibling"); err != nil || sibling.Key != "sibling-token" {
		t.Fatalf("sibling=%#v err=%v", sibling, err)
	}
}

func TestLogoutExplicitScopePreservesCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "current", Credential{Key: "current-token", Email: "user@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "sibling", Credential{Key: "sibling-token"}); err != nil {
		t.Fatal(err)
	}
	scope := "sibling"
	result, err := Logout(path, "current", &scope)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WasLoggedIn || result.ClearedCurrent {
		t.Fatalf("result=%#v", result)
	}
	if current, err := Load(path, "current"); err != nil || current.Key != "current-token" {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	if _, err := Load(path, "sibling"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sibling scope still loads: %v", err)
	}
}

func TestLogoutMissingCurrentDoesNotRemoveRequestedScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "sibling", Credential{Key: "sibling-token"}); err != nil {
		t.Fatal(err)
	}
	scope := "sibling"
	result, err := Logout(path, "missing", &scope)
	if err != nil || result.WasLoggedIn {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if sibling, err := Load(path, "sibling"); err != nil || sibling.Key != "sibling-token" {
		t.Fatalf("sibling=%#v err=%v", sibling, err)
	}
}

func TestLogoutPropagatesCorruptStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Logout(path, "current", nil); err == nil {
		t.Fatal("corrupt auth store unexpectedly succeeded")
	}
}
