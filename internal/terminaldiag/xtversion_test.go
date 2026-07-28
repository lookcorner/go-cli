package terminaldiag

import (
	"strings"
	"testing"
)

func TestSanitizeAndRecordXTVERSION(t *testing.T) {
	t.Cleanup(ResetXTVERSIONForTest)
	ResetXTVERSIONForTest()
	if _, ok := XTVERSION(); ok {
		t.Fatal("expected empty")
	}
	RecordXTVERSION(" We\x01zTerm 20240203 ")
	got, ok := XTVERSION()
	if !ok || got != "WezTerm 20240203" {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
	if !xtversionIsWezTerm(got) {
		t.Fatal("expected WezTerm prefix")
	}
	RecordXTVERSION(" \x07 ")
	if _, ok := XTVERSION(); ok {
		t.Fatal("empty payload should not identify")
	}
}

func TestShouldProbeXTVERSIONGate(t *testing.T) {
	if !ShouldProbeXTVERSION(func(string) string { return "" }) {
		t.Fatal("unknown brand should probe")
	}
	if !ShouldProbeXTVERSION(func(key string) string {
		if key == "TERM" {
			return "xterm-256color"
		}
		return ""
	}) {
		t.Fatal("plain xterm TERM should probe")
	}
	if !ShouldProbeXTVERSION(func(key string) string {
		if key == "TERM_PROGRAM" {
			return "WezTerm"
		}
		return ""
	}) {
		t.Fatal("wezterm should probe")
	}
	if ShouldProbeXTVERSION(func(key string) string {
		if key == "TERM_PROGRAM" {
			return "Apple_Terminal"
		}
		return ""
	}) {
		t.Fatal("apple terminal should not probe")
	}
	if ShouldProbeXTVERSION(func(key string) string {
		if key == "TMUX" {
			return "1"
		}
		return ""
	}) {
		t.Fatal("tmux should suppress probe")
	}
}

func TestBuildSnapshotWarnsForSSHWezTermXTVERSION(t *testing.T) {
	t.Cleanup(ResetXTVERSIONForTest)
	ResetXTVERSIONForTest()
	RecordXTVERSION("WezTerm 20240203-120000-abcdef")
	env := map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor",
		"SSH_CONNECTION": "1 2 3 4",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "linux")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{
		"WezTerm over SSH",
		`\`,
		"applies only to local WezTerm",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "wezterm.lua") {
		t.Fatalf("SSH finding should not recommend local lua: %q", joined)
	}
	if snapshot.Facts.XTVERSION != "WezTerm 20240203-120000-abcdef" {
		t.Fatalf("xtversion fact=%q", snapshot.Facts.XTVERSION)
	}
	human := snapshot.Human()
	if !strings.Contains(human, "xtversion    WezTerm 20240203-120000-abcdef") {
		t.Fatalf("human missing xtversion:\n%s", human)
	}
	env["TMUX"] = "1"
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "linux")
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "WezTerm over SSH") {
		t.Fatalf("tmux should suppress SSH wezterm finding: %#v", snapshot.Findings)
	}
}
