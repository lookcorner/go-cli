package config

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signedTestPolicy(t *testing.T, private ed25519.PrivateKey, payload signedPayload, hint string) signatureEnvelope {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return signatureEnvelope{
		SignedPayload: string(data),
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(private, data)),
		KeyID:         hint,
	}
}

func TestPolicySyncVerifiesAndPersistsSignedPolicy(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(2_000_000_000, 0)
	managed := "[models]\ndefault = \"managed\"\n"
	requirements := "fail_closed = true\n[auth]\npreferred_method = \"oidc\"\n"
	team := "team-1"
	payload := signedPayload{
		Version: 1, TeamID: &team, Managed: &managed, Requirements: &requirements,
		FailClosed: true, ExpiresAt: uint64(fixed.Add(time.Hour).Unix()), KeyID: "trusted",
	}
	trusted := signedTestPolicy(t, private, payload, "trusted")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-1" {
			t.Fatalf("authorization header=%q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(policyResponse{
			TeamID: team, Managed: &managed, Requirements: &requirements,
			Signatures: []signatureEnvelope{{KeyID: "retired"}, trusted},
		})
	}))
	defer server.Close()

	home := t.TempDir()
	client := NewPolicyClient(server.Client())
	client.keys = []PolicyKey{{ID: "trusted", Key: public}}
	client.now = func() time.Time { return fixed }
	changed, err := client.Sync(context.Background(), home, server.URL, "access-1", team, "")
	if err != nil || !changed {
		t.Fatalf("sync changed=%v err=%v", changed, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "managed_config.toml")); err != nil || string(data) != managed {
		t.Fatalf("managed config=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "requirements.toml")); err != nil || string(data) != requirements {
		t.Fatalf("requirements=%q err=%v", data, err)
	}
	if err := verifyManagedPolicy(home, team, "", client.keys, uint64(fixed.Unix())); err != nil {
		t.Fatalf("persisted policy did not verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedPolicy(home, team, "", client.keys, uint64(fixed.Unix())); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered policy was accepted: %v", err)
	}
}

func TestPolicySyncRejectsUnsignedOrMismatchedResponse(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(2_000_000_000, 0)
	signedManaged, servedManaged, team := "signed", "served", "team-1"
	payload := signedPayload{
		TeamID: &team, Managed: &signedManaged, ExpiresAt: uint64(fixed.Add(time.Hour).Unix()), KeyID: "trusted",
	}
	tests := []policyResponse{
		{Managed: &servedManaged},
		{Managed: &servedManaged, Signatures: []signatureEnvelope{signedTestPolicy(t, private, payload, "trusted")}},
	}
	for _, body := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(writer).Encode(body)
		}))
		home := t.TempDir()
		client := NewPolicyClient(server.Client())
		client.keys = []PolicyKey{{ID: "trusted", Key: public}}
		client.now = func() time.Time { return fixed }
		_, err := client.Sync(context.Background(), home, server.URL, "token", team, "")
		server.Close()
		if err == nil {
			t.Fatal("invalid signed response was accepted")
		}
		if _, err := os.Stat(filepath.Join(home, "managed_config.toml")); !os.IsNotExist(err) {
			t.Fatalf("rejected response changed disk: %v", err)
		}
	}
}

func TestManagedPolicyFailClosedSurvivesSidecarAndFileRemoval(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(2_000_000_000, 0)
	managed, deployment := "managed", "deployment-1"
	payload := signedPayload{
		DeploymentID: &deployment, Managed: &managed, FailClosed: true,
		ExpiresAt: uint64(fixed.Add(time.Hour).Unix()), KeyID: "trusted",
	}
	envelope := signedTestPolicy(t, private, payload, "trusted")
	home := t.TempDir()
	if err := writeJSONAtomic(filepath.Join(home, policySignatureFile), envelope); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(home, policyMarkerFile), policyMarker{Principal: deployment, FailClosed: true, HadManaged: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "managed_config.toml"), []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := []PolicyKey{{ID: "trusted", Key: public}}
	if err := verifyManagedPolicy(home, deployment, "", keys, uint64(fixed.Unix())); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, policySignatureFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "managed_config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedPolicy(home, deployment, "", keys, uint64(fixed.Unix())); err == nil {
		t.Fatal("removing signed fail-closed policy disabled enforcement")
	}
}

func TestManagedPolicyEndpointResolution(t *testing.T) {
	cfg := Config{ProxyBaseURL: "https://proxy.example/v1/"}
	if got := cfg.ManagedPolicyURL(); got != "https://proxy.example/v1/deployment/config" {
		t.Fatalf("default managed policy URL=%q", got)
	}
	cfg.ManagedConfigURL = " https://managed.example/config "
	if got := cfg.ManagedPolicyURL(); got != "https://managed.example/config" {
		t.Fatalf("explicit managed policy URL=%q", got)
	}
}

func TestPolicyClientRejectsCrossOriginRedirect(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer server.Close()
	_, err := NewPolicyClient(server.Client()).Sync(context.Background(), t.TempDir(), server.URL, "secret", "", "")
	if err == nil || called {
		t.Fatalf("cross-origin redirect err=%v called=%v", err, called)
	}
}
