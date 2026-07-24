package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/update"
	"github.com/lookcorner/go-cli/internal/version"
)

func TestUpdateCheckReportsPrivacyPolicy(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runUpdate([]string{"--check"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Gork Build - v" + version.Current + " [stable]", "Auto-update: disabled", update.ReleasesURL} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestUpdateCheckJSONAndChannelSwitch(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runUpdate([]string{"--check", "--json", "--alpha"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var status update.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != version.Current || status.Channel != "alpha" ||
		status.UpdateAvailable || status.AutoUpdate == nil || *status.AutoUpdate ||
		status.LatestVersion != nil || status.Installer != nil || status.Error != nil {
		t.Fatalf("status=%#v", status)
	}
	if stderr.String() != "Switched to alpha channel.\n" {
		t.Fatalf("stderr=%q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := runUpdate([]string{"--check", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.Channel != "alpha" {
		t.Fatalf("persisted status=%#v err=%v", status, err)
	}
	stdout.Reset()
	if err := runUpdate([]string{"--check", "--alpha"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unchanged channel stderr=%q", stderr.String())
	}
}

func TestUpdateInstallIsHardBlocked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	for _, args := range [][]string{nil, {"--force-reinstall"}, {"--version", "0.1.2"}, {"--alpha"}} {
		err := runUpdate(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "never installs from vendor") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
	var stdout bytes.Buffer
	if err := runUpdate([]string{"--check", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var status update.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.Channel != "stable" {
		t.Fatalf("blocked install changed channel: status=%#v err=%v", status, err)
	}
}

func TestUpdateRejectsInvalidArguments(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--json"}, "--check"},
		{[]string{"--check", "--version", "0.1.2"}, "--version"},
		{[]string{"--version", "bad"}, "valid version"},
		{[]string{"--version", "v0.1.2"}, "valid version"},
		{[]string{"--alpha", "--stable"}, "mutually exclusive"},
		{[]string{"extra"}, "positional"},
	} {
		if err := runUpdate(test.args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("args=%v err=%v want=%q", test.args, err, test.want)
		}
	}
}

func TestSelectedUpdateChannel(t *testing.T) {
	for _, test := range []struct {
		alpha, stable, enterprise bool
		want                      string
	}{
		{want: ""},
		{alpha: true, want: "alpha"},
		{stable: true, want: "stable"},
		{enterprise: true, want: "enterprise"},
	} {
		got, err := selectedUpdateChannel(test.alpha, test.stable, test.enterprise)
		if err != nil || got != test.want {
			t.Fatalf("selection=%+v got=%q err=%v", test, got, err)
		}
	}
}

func TestUpdateHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runUpdate([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Usage: gork update") {
		t.Fatalf("help=%q", stderr.String())
	}
	if strings.Contains(stderr.String(), "enterprise") {
		t.Fatalf("hidden enterprise flag leaked into help: %q", stderr.String())
	}
}

func TestUpdateDispatchesBeforeAgentConfiguration(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"update", "--check", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var status update.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.Channel != "stable" {
		t.Fatalf("status=%#v err=%v output=%q", status, err, stdout.String())
	}
}
