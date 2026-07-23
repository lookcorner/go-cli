package terminaldiag

import (
	"errors"
	"strings"
	"testing"
)

func TestIsCommand(t *testing.T) {
	for _, prompt := range []string{"/terminal-setup", " /terminal-check ignored ", "/terminal-info"} {
		if !IsCommand(prompt) {
			t.Errorf("command not recognized: %q", prompt)
		}
	}
	for _, prompt := range []string{"", "/terminal", "/terminal-setupx", "terminal-setup"} {
		if IsCommand(prompt) {
			t.Errorf("non-command recognized: %q", prompt)
		}
	}
}

func TestBuildReportDescribesTerminalAndRoutes(t *testing.T) {
	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor",
		"TMUX": "/tmp/tmux", "SSH_CONNECTION": "client server",
	}
	report := buildReport(func(key string) string { return env[key] }, func(name string) (string, error) {
		if name == "pbcopy" {
			return "/usr/bin/pbcopy", nil
		}
		return "", errors.New("missing")
	}, "darwin")
	for _, want := range []string{"terminal     WezTerm", "multiplexer  tmux", "ssh          yes", "color        truecolor", "native       active (tool: pbcopy)", "osc 52       active", "No issues found."} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildReportExplainsDegradedEnvironment(t *testing.T) {
	env := map[string]string{"TERM": "dumb"}
	report := buildReport(func(key string) string { return env[key] }, func(string) (string, error) {
		return "", errors.New("missing")
	}, "linux")
	for _, want := range []string{"terminal     dumb", "color        none", "native       off", "osc 52       off", "2 issue(s)", "TERM=dumb", "No clipboard route"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildReportWarnsForBasicTmuxColor(t *testing.T) {
	env := map[string]string{"TERM": "screen", "TMUX": "yes", "BYOBU_BACKEND": "tmux"}
	report := buildReport(func(key string) string { return env[key] }, func(name string) (string, error) {
		if name == "wl-copy" {
			return "/usr/bin/wl-copy", nil
		}
		return "", errors.New("missing")
	}, "linux")
	for _, want := range []string{"multiplexer  byobu (tmux)", "color        basic", "2 issue(s)", "COLORTERM=truecolor", "tmux-256color"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestTerminalDetectionVariants(t *testing.T) {
	tests := []struct {
		env               map[string]string
		brand, mux, color string
	}{
		{map[string]string{"WT_SESSION": "id"}, "Windows Terminal", "none", "truecolor"},
		{map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty", "COLORTERM": "24bit", "ZELLIJ": "0"}, "Kitty", "zellij", "truecolor"},
		{map[string]string{"TERM": "xterm-256color", "STY": "screen"}, "xterm-256color", "screen", "256 colors"},
		{map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, "xterm", "none", "none"},
		{map[string]string{}, "unknown", "none", "none"},
	}
	for _, test := range tests {
		getenv := func(key string) string { return test.env[key] }
		term := getenv("TERM")
		if brand := terminalBrand(getenv, term); brand != test.brand {
			t.Errorf("env=%v brand=%q want=%q", test.env, brand, test.brand)
		}
		if mux := terminalMultiplexer(getenv); mux != test.mux {
			t.Errorf("env=%v mux=%q want=%q", test.env, mux, test.mux)
		}
		if color := colorSupport(getenv, term); color != test.color {
			t.Errorf("env=%v color=%q want=%q", test.env, color, test.color)
		}
	}
}

func TestNativeClipboardCandidates(t *testing.T) {
	for _, test := range []struct {
		goos, installed, want string
	}{
		{"darwin", "pbcopy", "pbcopy"},
		{"windows", "clip", "clip"},
		{"linux", "wl-copy", "wl-copy"},
		{"linux", "xclip", "xclip"},
		{"linux", "xsel", "xsel"},
	} {
		found, tool := nativeClipboard(func(name string) (string, error) {
			if name == test.installed {
				return name, nil
			}
			return "", errors.New("missing")
		}, test.goos)
		if !found || tool != test.want {
			t.Errorf("goos=%q installed=%q found=%v tool=%q", test.goos, test.installed, found, tool)
		}
	}
	found, tool := nativeClipboard(func(string) (string, error) { return "", errors.New("missing") }, "linux")
	if found || tool != "" {
		t.Fatalf("missing clipboard found=%v tool=%q", found, tool)
	}
}
