package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceSyncFallsBackToLegacyBundle(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" || request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-email") != "user@example.com" {
			t.Errorf("headers=%v", request.Header)
		}
		if request.URL.Path == "/v1/bundle/archive" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Path != "/v1/subagents/bundle" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(Payload{
			Version: "v1", Personas: map[string]string{"researcher": "instructions = \"First paragraph.\\n\\nSecond.\"\n[[inputs]]\nname = \"topic\"\n"},
			Roles: map[string]string{"reviewer": "description = \"Reviews code\"\n"}, Agents: map[string]string{"worker": "# Worker\n"},
			Skills: map[string]string{"commit": "# Commit\n"},
		})
	}))
	defer server.Close()

	service := &Service{
		Root: t.TempDir(), BaseURL: server.URL + "/v1", HTTP: server.Client(),
		Credentials: func(context.Context, string) (Credentials, error) {
			return Credentials{Token: "token", UserID: "user-1", Email: "user@example.com"}, nil
		},
	}
	result, err := service.Sync(context.Background(), false)
	if err != nil || requests.Load() != 2 || result.Version != "v1" || result.PersonasCount != 1 || result.RolesCount != 1 || result.AgentsCount != 1 || result.SkillsCount != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	status, err := service.Status()
	if err != nil || !status.HasCache || status.Version == nil || *status.Version != "v1" || len(status.PersonaDetails) != 1 || status.PersonaDetails[0].Description == nil || *status.PersonaDetails[0].Description != "First paragraph." || !status.PersonaDetails[0].HasInputs || status.PersonaDetails[0].HasOutputs || status.RoleDetails[0].Description != "Reviews code" || status.Skills[0] != "commit" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	entry, err := service.Entry("agent", "worker")
	if err != nil || entry.Content != "# Worker\n" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
}

func TestServiceSyncUsesArchiveAndDeploymentHeaders(t *testing.T) {
	archive := testArchive(t, map[string][]byte{
		"bundle.json":              []byte(`{"version":"archive-v1"}`),
		"subagents/agents/a.md":    []byte("# A\n"),
		"skills/review/SKILL.md":   []byte("# Review\n"),
		"../../outside":            []byte("ignored"),
		"subagents/agents/../x.md": []byte("ignored"),
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bundle/archive" || request.Header.Get("Authorization") != "Bearer deployment" || request.Header.Get("X-XAI-Token-Auth") != "" || request.Header.Get("x-userid") != "" {
			t.Errorf("request path=%q headers=%v", request.URL.Path, request.Header)
		}
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	root := t.TempDir()
	service := &Service{Root: root, BaseURL: server.URL, HTTP: server.Client(), Credentials: func(context.Context, string) (Credentials, error) {
		return Credentials{Token: "deployment", Deployment: true}, nil
	}}
	result, err := service.Sync(context.Background(), true)
	if err != nil || result.Version != "archive-v1" || result.AgentsCount != 1 || result.SkillsCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "a.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
		t.Fatalf("traversal file err=%v", err)
	}
}

func TestServiceArchiveUnauthorizedDoesNotFallback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	service := &Service{Root: t.TempDir(), BaseURL: server.URL, HTTP: server.Client(), Credentials: func(context.Context, string) (Credentials, error) {
		return Credentials{Token: "token"}, nil
	}}
	if _, err := service.Sync(context.Background(), false); err == nil || !strings.Contains(err.Error(), "401") || requests.Load() != 2 {
		t.Fatalf("requests=%d err=%v", requests.Load(), err)
	}
}

func TestServiceRefreshesRejectedSessionTokenOnce(t *testing.T) {
	archive := testArchive(t, map[string][]byte{"bundle.json": []byte(`{"version":"refreshed"}`)})
	var requests, credentials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") == "Bearer stale" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	service := &Service{Root: t.TempDir(), BaseURL: server.URL, HTTP: server.Client(), Credentials: func(_ context.Context, rejected string) (Credentials, error) {
		credentials.Add(1)
		if rejected == "stale" {
			return Credentials{Token: "fresh"}, nil
		}
		return Credentials{Token: "stale"}, nil
	}}
	result, err := service.Sync(context.Background(), false)
	if err != nil || result.Version != "refreshed" || requests.Load() != 2 || credentials.Load() != 2 {
		t.Fatalf("result=%#v requests=%d credentials=%d err=%v", result, requests.Load(), credentials.Load(), err)
	}
}

func TestCachePreservesModifiedFilesAndPrunesManagedFiles(t *testing.T) {
	root := t.TempDir()
	first, err := writePayload(root, Payload{Version: "v1", Agents: map[string]string{"worker": "managed"}, Roles: map[string]string{"old": "description='old'"}})
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(root, "agents", "worker.md")
	if err := os.WriteFile(worker, []byte("local edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := writePayload(root, Payload{Version: "v2", Agents: map[string]string{"worker": "remote edit"}})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(worker)
	if string(content) != "local edit" || second.Checksums["agents/worker.md"] != first.Checksums["agents/worker.md"] {
		t.Fatalf("content=%q manifests=%#v %#v", content, first, second)
	}
	if _, err := os.Stat(filepath.Join(root, "roles", "old.toml")); !os.IsNotExist(err) {
		t.Fatalf("managed removed role err=%v", err)
	}
}

func TestCacheRejectsInvalidNamesAndSymlinkParents(t *testing.T) {
	root := t.TempDir()
	if _, err := writePayload(root, Payload{Version: "v1", Agents: map[string]string{"../escape": "bad"}}); err == nil {
		t.Fatal("invalid agent name was accepted")
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := updateCache(root, "v1", map[string][]byte{"skills/linked/SKILL.md": []byte("bad")}); err == nil {
		t.Fatal("symlink parent was accepted")
	}
}

func TestMaybeSyncSkipsMissingCredentialsAndFreshCache(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	service := &Service{Root: t.TempDir(), BaseURL: server.URL, HTTP: server.Client(), Credentials: func(context.Context, string) (Credentials, error) { return Credentials{}, nil }}
	if result, err := service.MaybeSync(context.Background()); err != nil || result != nil || requests.Load() != 0 {
		t.Fatalf("missing credentials result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	if _, err := writePayload(service.Root, Payload{Version: "fresh"}); err != nil {
		t.Fatal(err)
	}
	service.Credentials = func(context.Context, string) (Credentials, error) { return Credentials{Token: "token"}, nil }
	if result, err := service.MaybeSync(context.Background()); err != nil || result != nil || requests.Load() != 0 {
		t.Fatalf("fresh result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}

func TestReadManifestRejectsOversizedAndTrailingData(t *testing.T) {
	for name, content := range map[string][]byte{
		"oversized": append([]byte(`{"version":"v1","checksums":{}}`), bytes.Repeat([]byte(" "), maxManifestBytes)...),
		"trailing":  []byte(`{"version":"v1","checksums":{}} garbage`),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "manifest.json"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readManifest(root); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}
}

func TestFreshManifestRejectsFutureAndMalformedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":"v1","checksums":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if freshManifest(root, time.Hour) {
		t.Fatal("future manifest was considered fresh")
	}
	if err := os.WriteFile(path, []byte(`{not json}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if freshManifest(root, time.Hour) {
		t.Fatal("malformed manifest was considered fresh")
	}
}

func TestArchiveLimits(t *testing.T) {
	t.Run("entry size", func(t *testing.T) {
		archive := testArchive(t, map[string][]byte{
			"bundle.json":           []byte(`{"version":"v1"}`),
			"subagents/agents/a.md": bytes.Repeat([]byte("a"), maxArchiveEntryBytes+1),
		})
		if _, err := extractArchive(t.TempDir(), archive); err == nil || !strings.Contains(err.Error(), "maximum size") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("entry count", func(t *testing.T) {
		entries := map[string][]byte{"bundle.json": []byte(`{"version":"v1"}`)}
		for index := 0; index < maxArchiveEntries; index++ {
			entries[fmt.Sprintf("ignored/%d", index)] = nil
		}
		archive := testArchive(t, entries)
		if _, err := extractArchive(t.TempDir(), archive); err == nil || !strings.Contains(err.Error(), "entry count") {
			t.Fatalf("err=%v", err)
		}
	})
}

func testArchive(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := gzip.NewWriter(&output)
	writer := tar.NewWriter(encoder)
	for name, content := range entries {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
