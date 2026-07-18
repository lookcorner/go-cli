package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(string(data))),
	}
}

func TestDeviceLoginProtocol(t *testing.T) {
	fixed := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	polls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-grok-client-surface") != "cli" || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("missing OAuth headers: %#v", request.Header)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch request.URL.Path {
		case "/oauth2/device/code":
			if request.Form.Get("client_id") != "client-1" || request.Form.Get("scope") != "openid offline_access" || request.Form.Get("referrer") != "grok-build" {
				t.Fatalf("unexpected device form: %#v", request.Form)
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"device_code": "device-1", "user_code": "ABCD-1234",
				"verification_uri": "https://accounts.x.ai/device", "expires_in": 600, "interval": 1,
			}), nil
		case "/oauth2/token":
			polls++
			if request.Form.Get("grant_type") != deviceGrantType || request.Form.Get("device_code") != "device-1" {
				t.Fatalf("unexpected token form: %#v", request.Form)
			}
			if polls == 1 {
				return jsonResponse(http.StatusBadRequest, map[string]any{"error": "authorization_pending"}), nil
			}
			claims, _ := json.Marshal(map[string]string{"sub": "user-1", "email": "user@example.com"})
			idToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
			return jsonResponse(http.StatusOK, map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600, "id_token": idToken,
			}), nil
		default:
			t.Fatalf("unexpected OAuth path %q", request.URL.Path)
			return nil, nil
		}
	})}
	client := NewClient(httpClient)
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	client.Now = func() time.Time { return fixed }
	cfg := Config{Issuer: "https://auth.example", ClientID: "client-1", Scopes: []string{"openid", "offline_access"}}
	code, err := client.RequestDeviceCode(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := client.CompleteDeviceLogin(context.Background(), cfg, code)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 2 || credential.Key != "access-1" || credential.RefreshToken != "refresh-1" || credential.UserID != "user-1" || credential.Email != "user@example.com" || credential.ExpiresAt == nil || !credential.ExpiresAt.Equal(fixed.Add(time.Hour)) {
		t.Fatalf("unexpected credential: %#v polls=%d", credential, polls)
	}
}

func TestResolveRefreshesAndPersistsCredential(t *testing.T) {
	fixed := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	cfg := Config{Issuer: "https://auth.example", ClientID: "client-1", Scopes: defaultScopes}
	expires := fixed.Add(time.Minute)
	if err := os.WriteFile(path, []byte(`{"sibling":{"key":"keep","custom":"preserved"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg.Scope(), Credential{Key: "old", RefreshToken: "refresh-1", ExpiresAt: &expires, UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "refresh-1" {
			t.Fatalf("unexpected refresh form: %#v", request.Form)
		}
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "new", "expires_in": 3600}), nil
	})}
	client := NewClient(httpClient)
	client.Now = func() time.Time { return fixed }
	token, err := client.Resolve(context.Background(), path, cfg)
	if err != nil || token != "new" {
		t.Fatalf("resolve token=%q err=%v", token, err)
	}
	credential, err := Load(path, cfg.Scope())
	if err != nil || credential.RefreshToken != "refresh-1" || credential.UserID != "user-1" {
		t.Fatalf("persisted refresh=%#v err=%v", credential, err)
	}
	if sibling, err := Load(path, "sibling"); err != nil || sibling.Key != "keep" {
		t.Fatalf("sibling credential was lost: %#v err=%v", sibling, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"custom": "preserved"`) {
		t.Fatalf("unknown auth fields were lost: %s err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth permissions=%v", info.Mode().Perm())
	}
}

func TestSavePreservesUnknownCredentialFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"scope":{"key":"old","team_name":"Core"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "scope", Credential{Key: "new", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"team_name": "Core"`) {
		t.Fatalf("unknown credential fields were lost: %s err=%v", data, err)
	}
}

func TestRemoveDeletesOnlySelectedScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := Save(path, "first", Credential{Key: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "second", Credential{Key: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "first"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed scope still loads: %v", err)
	}
	if credential, err := Load(path, "second"); err != nil || credential.Key != "two" {
		t.Fatalf("sibling scope=%#v err=%v", credential, err)
	}
	if err := Remove(path, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty auth store still exists: %v", err)
	}
	if err := Remove(path, "second"); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
}

func TestSaveBacksUpCorruptAuthStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "scope", Credential{Key: "token"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".corrupt.*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt backup=%#v err=%v", matches, err)
	}
	if credential, err := Load(path, "scope"); err != nil || credential.Key != "token" {
		t.Fatalf("recovered credential=%#v err=%v", credential, err)
	}
}

func TestVerificationURIRejectsUnsafeSchemes(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "http://example.com/device", "https://user@example.com/device"} {
		if validateVerificationURI(raw) == nil {
			t.Fatalf("unsafe verification URI accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://accounts.x.ai/device", "http://127.0.0.1:1234/device"} {
		if err := validateVerificationURI(raw); err != nil {
			t.Fatalf("valid verification URI rejected: %s: %v", raw, err)
		}
	}
}

func TestOAuthClientRejectsCrossOriginRedirects(t *testing.T) {
	client := NewClient(http.DefaultClient)
	first, _ := http.NewRequest(http.MethodPost, "https://auth.x.ai/oauth2/token", nil)
	same, _ := http.NewRequest(http.MethodPost, "https://auth.x.ai/other", nil)
	other, _ := http.NewRequest(http.MethodPost, "https://evil.example/token", nil)
	if err := client.HTTP.CheckRedirect(same, []*http.Request{first}); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
	if err := client.HTTP.CheckRedirect(other, []*http.Request{first}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("cross-origin redirect accepted: %v", err)
	}
}

func TestDefaultConfigRequiresCompleteEnvironmentPair(t *testing.T) {
	t.Setenv("GROK_OAUTH2_ISSUER", "https://custom.example")
	t.Setenv("GROK_OAUTH2_CLIENT_ID", "")
	t.Setenv("GROK_OIDC_ISSUER", "")
	t.Setenv("GROK_OIDC_CLIENT_ID", "")
	if cfg := DefaultConfig(); cfg.Issuer != defaultIssuer || cfg.ClientID != defaultClientID {
		t.Fatalf("partial OAuth pair did not fall back: %#v", cfg)
	}
	t.Setenv("GROK_OAUTH2_CLIENT_ID", "custom-client")
	if cfg := DefaultConfig(); cfg.Issuer != "https://custom.example" || cfg.ClientID != "custom-client" {
		t.Fatalf("complete OAuth pair was not used: %#v", cfg)
	}
}

func TestConcurrentResolveRefreshesOnce(t *testing.T) {
	fixed := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	cfg := Config{Issuer: "https://auth.example", ClientID: "client-1", Scopes: defaultScopes}
	expires := fixed.Add(time.Hour)
	if err := Save(path, cfg.Scope(), Credential{Key: "old", RefreshToken: "refresh-1", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		refreshes.Add(1)
		time.Sleep(25 * time.Millisecond)
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "new", "expires_in": 3600}), nil
	})}
	start := make(chan struct{})
	results := make(chan string, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client := NewClient(httpClient)
			client.Now = func() time.Time { return fixed }
			<-start
			token, err := client.ResolveRejected(context.Background(), path, cfg, "old")
			results <- token
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for token := range results {
		if token != "new" {
			t.Fatalf("resolved token=%q", token)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh requests=%d", refreshes.Load())
	}
}

func TestSaveRecoversStaleAuthLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	lockPath := filepath.Join(dir, "auth.json.lock")
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleLockAge - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "scope", Credential{Key: "token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auth lock was not released: %v", err)
	}
}

func TestAuthLockWaitCanBeCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	lock, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireFileLock(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock wait error=%v", err)
	}
}

func TestFreshCredentialDoesNotWaitForAuthLock(t *testing.T) {
	fixed := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	cfg := Config{Issuer: "https://auth.example", ClientID: "client-1"}
	expires := fixed.Add(time.Hour)
	if err := Save(path, cfg.Scope(), Credential{Key: "fresh", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	client := NewClient(http.DefaultClient)
	client.Now = func() time.Time { return fixed }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if token, err := client.Resolve(ctx, path, cfg); err != nil || token != "fresh" {
		t.Fatalf("fresh token=%q err=%v", token, err)
	}
}
