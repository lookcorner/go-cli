package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/version"
)

func TestVersionCLIHumanAndJSON(t *testing.T) {
	current := version.Current
	version.Current = "0.1.2-alpha.3"
	t.Cleanup(func() { version.Current = current })
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "version.json"), []byte(`{"stable_version":"0.1.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runVersion(nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "gork 0.1.2-alpha.3 [alpha]\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := runVersion([]string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil ||
		payload["currentVersion"] != "0.1.2-alpha.3" || payload["channel"] != "alpha" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func TestVersionSubcommandDispatchesBeforeConfiguration(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"version", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if json.Unmarshal(stdout.Bytes(), &payload) != nil || payload["currentVersion"] == "" || payload["channel"] != "unknown" || stderr.Len() != 0 {
		t.Fatalf("payload=%#v stderr=%q", payload, stderr.String())
	}
}

func TestVersionCLIRejectsArguments(t *testing.T) {
	if err := runVersion([]string{"extra"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("version accepted positional arguments")
	}
}

func TestVersionCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runVersion([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage: gork version [--json]") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
