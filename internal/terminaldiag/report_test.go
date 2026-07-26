package terminaldiag

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/voice"
)

func init() {
	// Keep doctor report tests independent of the host microphone.
	probeVoiceInput = func(func(string) (string, error)) voice.InputProbe {
		return voice.InputProbe{}
	}
}

func TestIsCommand(t *testing.T) {
	for _, prompt := range []string{"/doctor", "/terminal-setup", " /terminal-check ignored ", "/terminal-info"} {
		if !IsCommand(prompt) {
			t.Errorf("command not recognized: %q", prompt)
		}
	}
	for _, prompt := range []string{"", "/terminal", "/terminal-setupx", "terminal-setup", "doctor"} {
		if IsCommand(prompt) {
			t.Errorf("non-command recognized: %q", prompt)
		}
	}
}

func TestBuildSnapshotJSONShape(t *testing.T) {
	env := map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor"}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "", errors.New("missing")
	}, "linux")
	if snapshot.SchemaVersion != SchemaVersion || snapshot.Facts.Terminal != "WezTerm" || snapshot.Counts.Issues != 0 || !snapshot.Facts.OSC52 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || !strings.Contains(string(payload), `"schemaVersion":"1"`) || !strings.Contains(string(payload), `"findings"`) {
		t.Fatalf("json=%s err=%v", payload, err)
	}
}

func TestBuildReportDescribesTerminalAndRoutes(t *testing.T) {
	stubHealthyTmuxProbes(t)

	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor",
		"TMUX": "/tmp/tmux", "SSH_CONNECTION": "client server", "GROK_OSC52_SINK": "1",
	}
	report := buildReport(func(key string) string { return env[key] }, func(name string) (string, error) {
		if name == "pbcopy" {
			return "/usr/bin/pbcopy", nil
		}
		return "", errors.New("missing")
	}, "darwin")
	for _, want := range []string{
		"terminal     WezTerm", "multiplexer  tmux", "ssh          yes", "color        truecolor",
		"set-clipboard external", "allow-passthrough on", "extended-keys on", "control-mode off",
		"native       active (tool: pbcopy)", "osc 52       active", "No issues found.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildReportRecommendsSSHWrap(t *testing.T) {
	env := map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor",
		"SSH_CONNECTION": "1 2 3 4", "TERM_PROGRAM": "WezTerm",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Counts.Issues != 1 || !strings.Contains(strings.Join(snapshot.Findings, "\n"), "gork doctor fix ssh-wrap") {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	env["GROK_OSC52_SINK"] = "1"
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Counts.Issues != 0 {
		t.Fatalf("wrap sink should silence hint: %#v", snapshot)
	}
}

func TestSSHWrapRecommendedMatchesRemoteTransportShape(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "plain ssh", env: map[string]string{"SSH_CONNECTION": "1 2 3 4"}, want: true},
		{name: "ssh tty", env: map[string]string{"SSH_TTY": "/dev/pts/1"}, want: true},
		{name: "local", env: map[string]string{}},
		{name: "wrapped sink", env: map[string]string{"SSH_CONNECTION": "1", "GROK_OSC52_SINK": "1"}},
		{name: "forwarded sink", env: map[string]string{"SSH_CONNECTION": "1", "LC_GROK_OSC52_SINK": "1"}},
		{name: "vscode remote", env: map[string]string{"SSH_CONNECTION": "1", "TERM_PROGRAM": "vscode"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sshWrapRecommended(func(key string) string { return test.env[key] }); got != test.want {
				t.Fatalf("recommended=%v want=%v env=%#v", got, test.want, test.env)
			}
		})
	}
}

func TestBuildReportWarnsForByobuScreen(t *testing.T) {
	prevOpt, prevControl := probeTmuxOption, probeTmuxControlMode
	probeTmuxOption = func(string, bool) (string, bool) {
		t.Fatal("tmux options should not be probed under byobu screen")
		return "", false
	}
	probeTmuxControlMode = func() (string, bool) {
		t.Fatal("tmux control mode should not be probed under byobu screen")
		return "", false
	}
	t.Cleanup(func() {
		probeTmuxOption = prevOpt
		probeTmuxControlMode = prevControl
	})

	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor",
		"STY": "123.pts", "BYOBU_BACKEND": "screen",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/usr/bin/wl-copy", nil
	}, "linux")
	if snapshot.Facts.Multiplexer != "byobu (screen)" || snapshot.Counts.Issues != 1 || !strings.Contains(snapshot.Findings[0], "GNU screen") {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestBuildReportWarnsForUnhealthyTmuxOptions(t *testing.T) {
	prevOpt, prevControl := probeTmuxOption, probeTmuxControlMode
	probeTmuxOption = func(option string, _ bool) (string, bool) {
		switch option {
		case "set-clipboard":
			return "off", true
		case "allow-passthrough":
			return "off", true
		case "extended-keys":
			return "off", true
		default:
			return "", false
		}
	}
	probeTmuxControlMode = func() (string, bool) { return "off", true }
	t.Cleanup(func() {
		probeTmuxOption = prevOpt
		probeTmuxControlMode = prevControl
	})

	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor", "TMUX": "yes",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Counts.Issues != 3 {
		t.Fatalf("issues=%d findings=%v", snapshot.Counts.Issues, snapshot.Findings)
	}
	report := snapshot.Human()
	for _, want := range []string{"gork doctor fix tmux-clipboard", "gork doctor fix dcs-passthrough", "gork doctor fix tmux-extended-keys"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildReportWarnsForTmuxControlMode(t *testing.T) {
	prevOpt, prevControl := probeTmuxOption, probeTmuxControlMode
	probeTmuxOption = func(option string, _ bool) (string, bool) {
		switch option {
		case "set-clipboard":
			return "external", true
		case "allow-passthrough":
			return "on", true
		case "extended-keys":
			return "on", true
		default:
			return "", false
		}
	}
	probeTmuxControlMode = func() (string, bool) { return "on", true }
	t.Cleanup(func() {
		probeTmuxOption = prevOpt
		probeTmuxControlMode = prevControl
	})

	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor", "TMUX": "yes",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Facts.ControlMode != "on" || snapshot.Counts.Issues != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{"Display may be limited in tmux control mode", "regular tmux client"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestBuildReportExplainsDegradedEnvironment(t *testing.T) {
	env := map[string]string{"TERM": "dumb"}
	report := buildReport(func(key string) string { return env[key] }, func(string) (string, error) {
		return "", errors.New("missing")
	}, "linux")
	for _, want := range []string{
		"terminal     dumb", "color        none", "native       off", "osc 52       off",
		"4 issue(s)", "TERM=dumb", "No clipboard route", "terminal bell", "unfocused",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildReportWarnsForBasicTmuxColor(t *testing.T) {
	prevOpt, prevControl := probeTmuxOption, probeTmuxControlMode
	probeTmuxOption = func(string, bool) (string, bool) { return "", false }
	probeTmuxControlMode = func() (string, bool) { return "", false }
	t.Cleanup(func() {
		probeTmuxOption = prevOpt
		probeTmuxControlMode = prevControl
	})

	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "screen", "TMUX": "yes", "BYOBU_BACKEND": "tmux",
	}
	report := buildReport(func(key string) string { return env[key] }, func(name string) (string, error) {
		if name == "wl-copy" {
			return "/usr/bin/wl-copy", nil
		}
		return "", errors.New("missing")
	}, "linux")
	for _, want := range []string{"multiplexer  byobu (tmux)", "color        basic", "2 issue(s)", "COLORTERM=truecolor", "tmux-256color", "gork doctor fix tmux-truecolor", "gork doctor fix colorterm"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildSnapshotWarnsForNoColor(t *testing.T) {
	env := map[string]string{
		"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Facts.Color != "none" || snapshot.Counts.Issues != 1 ||
		!strings.Contains(snapshot.Findings[0], "NO_COLOR") ||
		strings.Contains(strings.Join(snapshot.Findings, "\n"), "colorterm") {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestBuildSnapshotWarnsForAppleTerminalLimits(t *testing.T) {
	env := map[string]string{
		"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color",
		"SSH_CONNECTION": "1 2 3 4",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{
		"Apple Terminal supports 256 colors",
		"doesn't support OSC 52",
		"gork wrap ssh",
		"may not report focus changes",
		`condition = "always"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "colorterm") || strings.Contains(joined, "terminal bell") {
		t.Fatalf("Apple Terminal should not recommend COLORTERM or bell fallback: %q", joined)
	}
}

func TestBuildSnapshotWarnsForNotificationDefaults(t *testing.T) {
	env := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{"terminal bell", "may not report focus changes", `condition = "always"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if snapshot.Counts.Issues != 2 {
		t.Fatalf("issues=%d findings=%v", snapshot.Counts.Issues, snapshot.Findings)
	}
}

func TestSupportsFocusTracking(t *testing.T) {
	for _, brand := range []string{"kitty", "ghostty", "wezterm", "iterm2", "alacritty", "vscode", "vte"} {
		if !supportsFocusTracking(brand) {
			t.Errorf("%q should support focus tracking", brand)
		}
	}
	for _, brand := range []string{"", "appleterminal", "otty", "xterm", "testterm"} {
		if supportsFocusTracking(brand) {
			t.Errorf("%q should not support focus tracking", brand)
		}
	}
}

func TestBuildSnapshotWarnsForITerm2ClipboardPermission(t *testing.T) {
	env := map[string]string{
		"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color", "COLORTERM": "truecolor",
		"SSH_CONNECTION": "1 2 3 4",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined := strings.Join(snapshot.Findings, "\n")
	if !strings.Contains(joined, "iTerm2 may block OSC 52") || !strings.Contains(joined, "Applications in terminal may access clipboard") {
		t.Fatalf("missing iTerm2 clipboard finding: %q", joined)
	}
	env["GROK_OSC52_SINK"] = "1"
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "iTerm2 may block") {
		t.Fatalf("wrap sink should silence iTerm2 caveat: %#v", snapshot.Findings)
	}
}

func TestBuildSnapshotSilentForLocalITerm2WithNativeClipboard(t *testing.T) {
	env := map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color", "COLORTERM": "truecolor"}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "iTerm2 may block") {
		t.Fatalf("local native clipboard should skip iTerm2 caveat: %#v", snapshot.Findings)
	}
}

func TestBuildSnapshotWarnsForVSCodeSSHNonASCII(t *testing.T) {
	env := map[string]string{
		"TERM_PROGRAM": "vscode", "TERM": "xterm-256color", "COLORTERM": "truecolor",
		"SSH_CONNECTION": "1 2 3 4",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{
		"non-ASCII text copied with OSC 52", "`/minimal`",
		"Shift+Enter can't insert a newline in this xterm.js terminal", "Alt+Enter", "VS Code",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "ssh-wrap") {
		t.Fatalf("vscode remote should not recommend ssh-wrap: %q", joined)
	}
}

func TestBuildSnapshotWarnsForMissingVoiceInput(t *testing.T) {
	prev := probeVoiceInput
	probeVoiceInput = func(func(string) (string, error)) voice.InputProbe {
		return voice.InputProbe{
			Supported: true,
			Error:     "no microphone recorder found; install pw-record, parec, or arecord",
		}
	}
	t.Cleanup(func() { probeVoiceInput = prev })

	env := map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor"}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if snapshot.Facts.VoiceMicrophone != "none" || snapshot.Counts.Issues != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	report := snapshot.Human()
	for _, want := range []string{
		"Voice", "microphone   none detected",
		"Voice dictation is unavailable", "pw-record", "system sound settings",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildSnapshotReportsVoiceDevice(t *testing.T) {
	prev := probeVoiceInput
	probeVoiceInput = func(func(string) (string, error)) voice.InputProbe {
		return voice.InputProbe{
			Supported: true, Name: "parec", Detail: "system recorder; uses the audio server's default input",
		}
	}
	t.Cleanup(func() { probeVoiceInput = prev })

	env := map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color", "COLORTERM": "truecolor"}
	report := buildReport(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	for _, want := range []string{
		"microphone   parec (system recorder; uses the audio server's default input)",
		"No issues found.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in %q", want, report)
		}
	}
}

func TestBuildSnapshotWarnsForUnverifiedSSHClipboard(t *testing.T) {
	env := map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "SSH_CONNECTION": "1 2 3 4",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{
		"can't verify this clipboard route across the remote boundary",
		"gork wrap ssh", "gork doctor fix ssh-wrap", "`/minimal`",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	env["TERM_PROGRAM"] = "WezTerm"
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	joined = strings.Join(snapshot.Findings, "\n")
	if strings.Contains(joined, "can't verify this clipboard route") {
		t.Fatalf("known OSC 52 brand should not be unverified: %q", joined)
	}
	if !strings.Contains(joined, "gork doctor fix ssh-wrap") {
		t.Fatalf("ssh-wrap should remain for WezTerm SSH: %q", joined)
	}
	env["GROK_OSC52_SINK"] = "1"
	delete(env, "TERM_PROGRAM")
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/bin/pbcopy", nil
	}, "darwin")
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "can't verify this clipboard route") {
		t.Fatalf("wrap sink should silence unverified delivery: %#v", snapshot.Findings)
	}
}

func TestBuildSnapshotWarnsForLegacyVTENewline(t *testing.T) {
	env := map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "VTE_VERSION": "7402",
	}
	snapshot := BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/usr/bin/wl-copy", nil
	}, "linux")
	joined := strings.Join(snapshot.Findings, "\n")
	for _, want := range []string{
		"Shift+Enter can't insert a newline in this VTE terminal",
		"VTE 7402", "VTE 0.82", "Alt+Enter",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	env["VTE_VERSION"] = "8200"
	snapshot = BuildSnapshot(func(key string) string { return env[key] }, func(string) (string, error) {
		return "/usr/bin/wl-copy", nil
	}, "linux")
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "Shift+Enter") {
		t.Fatalf("modern VTE should not warn: %#v", snapshot.Findings)
	}
}

func TestTerminalDetectionVariants(t *testing.T) {
	tests := []struct {
		env               map[string]string
		brand, mux, color string
	}{
		{map[string]string{"WT_SESSION": "id"}, "Windows Terminal", "none", "truecolor"},
		{map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty", "COLORTERM": "24bit", "ZELLIJ": "0"}, "Kitty", "zellij", "truecolor"},
		{map[string]string{"LC_TERMINAL": "iTerm2", "TERM": "xterm-256color"}, "iTerm2", "none", "256 colors"},
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

func stubHealthyTmuxProbes(t *testing.T) {
	t.Helper()
	prevOpt, prevControl := probeTmuxOption, probeTmuxControlMode
	probeTmuxOption = func(option string, _ bool) (string, bool) {
		switch option {
		case "set-clipboard":
			return "external", true
		case "allow-passthrough":
			return "on", true
		case "extended-keys":
			return "on", true
		default:
			return "", false
		}
	}
	probeTmuxControlMode = func() (string, bool) { return "off", true }
	t.Cleanup(func() {
		probeTmuxOption = prevOpt
		probeTmuxControlMode = prevControl
	})
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
