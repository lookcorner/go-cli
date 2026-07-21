package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTerminalHyperlinksEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "wezterm", env: map[string]string{"TERM_PROGRAM": "WezTerm"}, want: true},
		{name: "iterm", env: map[string]string{"TERM_PROGRAM": "iTerm.app"}, want: true},
		{name: "vscode", env: map[string]string{"TERM_PROGRAM": "vscode"}, want: true},
		{name: "kitty term", env: map[string]string{"TERM": "xterm-kitty"}, want: true},
		{name: "term fallback", env: map[string]string{"TERM_PROGRAM": "custom", "TERM": "xterm-kitty"}, want: true},
		{name: "windows terminal", env: map[string]string{"WT_SESSION": "session"}, want: true},
		{name: "new vte", env: map[string]string{"VTE_VERSION": "7402"}, want: true},
		{name: "old vte", env: map[string]string{"VTE_VERSION": "4800"}},
		{name: "apple terminal", env: map[string]string{"TERM_PROGRAM": "Apple_Terminal"}},
		{name: "warp", env: map[string]string{"TERM_PROGRAM": "WarpTerminal"}},
		{name: "unknown", env: map[string]string{"TERM": "xterm-256color"}},
		{name: "multiplexer", env: map[string]string{"TERM_PROGRAM": "WezTerm", "TMUX": "/tmp/tmux"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				value, ok := test.env[name]
				return value, ok
			}
			if got := terminalHyperlinksEnabled(lookup); got != test.want {
				t.Fatalf("enabled=%v want=%v", got, test.want)
			}
		})
	}
}

func TestRenderMarkdownEmitsSafeOSC8Links(t *testing.T) {
	path := "/Users/alice/src/app/release/mac_arm64/Demo App.app"
	raw := strings.Join(renderMarkdownWithLinks(`Open "`+path+`" and [docs](https://example.com).`, 32, true), "\n")
	for _, target := range []string{fileHyperlink(path), "https://example.com"} {
		if !strings.Contains(raw, ansi.SetHyperlink(target, "id="+hyperlinkID(target))) {
			t.Fatalf("missing hyperlink %q in %q", target, raw)
		}
	}
	if !strings.Contains(raw, "Demo%20App.app") {
		t.Fatalf("file hyperlink was truncated: %q", raw)
	}
	if got, want := fileHyperlink(`C:\Program Files\Demo App.exe`), "file:///C:/Program%20Files/Demo%20App.exe"; got != want {
		t.Fatalf("windows file hyperlink=%q want=%q", got, want)
	}
	plain := strings.ReplaceAll(ansi.Strip(raw), "\n", "")
	if !strings.Contains(plain, path) {
		t.Fatalf("visible path changed: %q", plain)
	}
	for _, line := range strings.Split(raw, "\n") {
		if markdownANSIWidth(line) > 32 {
			t.Fatalf("linked line exceeded width: %q", line)
		}
	}

	disabled := strings.Join(renderMarkdownWithLinks(`[docs](https://example.com)`, 80, false), "\n")
	if strings.Contains(disabled, "\x1b]8;") {
		t.Fatalf("disabled renderer emitted OSC 8: %q", disabled)
	}
}

func TestRenderMarkdownRejectsUnsafeHyperlinks(t *testing.T) {
	for _, input := range []string{
		`[script](javascript:alert)`,
		"[control](https://example.com/\x1bpayload)",
		`[file](file:///etc/passwd)`,
		`[relative](/local/path)`,
	} {
		raw := strings.Join(renderMarkdownWithLinks(input, 80, true), "\n")
		if strings.Contains(raw, "\x1b]8;") {
			t.Fatalf("unsafe target emitted OSC 8 for %q: %q", input, raw)
		}
	}
}

func TestViewUsesDetectedHyperlinkCapability(t *testing.T) {
	m := &model{width: 100, height: 16, hyperlinks: true}
	m.transcript.WriteString(`[docs](https://example.com)`)
	target := "https://example.com"
	if content := m.View().Content; !strings.Contains(content, ansi.SetHyperlink(target, "id="+hyperlinkID(target))) {
		t.Fatalf("view did not emit hyperlink: %q", content)
	}
}
