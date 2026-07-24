package trace

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/session"
)

func TestServiceExportsOnlyRelatedSessionFiles(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "trace-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_started", map[string]any{"cwd": "/workspace"}); err != nil {
		t.Fatal(err)
	}
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 144, 119, 83, 222, 0, 0, 0, 12, 73, 68, 65, 84, 8, 215, 99, 248, 207, 192, 0, 0, 3, 1, 1, 0, 24, 221, 141, 176, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130}
	if err := logger.AppendPrompt("inspect", []session.Content{{
		Type: "image", URI: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	assetEntries, err := os.ReadDir(filepath.Join(sessionDir, "assets"))
	if err != nil || len(assetEntries) != 1 {
		t.Fatalf("assets=%v err=%v", assetEntries, err)
	}
	assetName := assetEntries[0].Name()
	if assetEntries[0].IsDir() {
		t.Fatal("session asset is a directory")
	}
	artifactDir, err := session.ArtifactDir(filepath.Join(sessionDir, "trace-one.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "result.txt"), []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "rewind"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "rewind", "trace-one.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "other.jsonl"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	result, err := (Service{SessionDir: sessionDir, ExportRoot: t.TempDir(), Now: func() time.Time { return now }}).Export("trace-one", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "trace-one" || result.Size == 0 {
		t.Fatalf("result=%#v", result)
	}
	names, contents := readArchive(t, result.Path)
	for _, want := range []string{
		"trace-one/trace-one.jsonl",
		"trace-one/assets/" + assetName,
		"trace-one/artifacts/result.txt",
		"trace-one/rewind.jsonl",
		"trace-one/trace_config.json",
		"trace-one/export_metadata.json",
	} {
		if !names[want] {
			t.Fatalf("archive missing %q: %#v", want, names)
		}
	}
	if names["trace-one/other.jsonl"] || names["other.jsonl"] {
		t.Fatal("archive included another session")
	}
	var config map[string]any
	if err := json.Unmarshal(contents["trace-one/trace_config.json"], &config); err != nil ||
		config["privacy_hard_off"] != true || config["trace_upload_enabled"] != false {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	info, err := os.Stat(result.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestServiceRejectsMissingInvalidAndSymlinkSessions(t *testing.T) {
	sessionDir := t.TempDir()
	service := Service{SessionDir: sessionDir, ExportRoot: t.TempDir()}
	if _, err := service.Export("../escape", ""); err == nil {
		t.Fatal("invalid session ID accepted")
	}
	if _, err := service.Export("missing", ""); err == nil {
		t.Fatal("missing session accepted")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(sessionDir, "target.jsonl")
		if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(sessionDir, "linked.jsonl")); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Export("linked", ""); err == nil {
			t.Fatal("symlink session accepted")
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "asset-link.jsonl"), []byte("{\"data\":{\"uri\":\"assets/outside.png\"}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "outside.png"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(sessionDir, "assets")); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Export("asset-link", ""); err == nil {
			t.Fatal("symlink asset directory accepted")
		}
	}
}

func TestServiceOverwritesExistingOutput(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_started", map[string]any{"cwd": "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "trace.tar.gz")
	if err := os.WriteFile(output, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (Service{SessionDir: sessionDir}).Export("overwrite", output)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != output {
		t.Fatalf("path=%q", result.Path)
	}
	readArchive(t, output)
}

func readArchive(t *testing.T, path string) (map[string]bool, map[string][]byte) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	names, contents := map[string]bool{}, map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name], contents[header.Name] = true, data
	}
	return names, contents
}
