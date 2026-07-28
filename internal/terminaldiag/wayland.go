package terminaldiag

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// DataControlFact mirrors Rust DataControlFact for doctor clipboard facts.
type DataControlFact string

const (
	DataControlAvailable     DataControlFact = "available"
	DataControlMissing       DataControlFact = "missing"
	DataControlUnavailable   DataControlFact = "unavailable"
	DataControlError         DataControlFact = "error"
	DataControlNotApplicable DataControlFact = "not_applicable"
)

// probeWaylandDataControl runs a bounded compositor probe. Tests replace this.
var probeWaylandDataControl = func(getenv func(string) string, lookPath func(string) (string, error)) (DataControlFact, string) {
	return probeWaylandDataControlWith(getenv, lookPath, runWaylandInfo)
}

func runWaylandInfo(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path)
	return cmd.CombinedOutput()
}

func probeWaylandDataControlWith(
	getenv func(string) string,
	lookPath func(string) (string, error),
	runInfo func(context.Context, string) ([]byte, error),
) (DataControlFact, string) {
	if strings.TrimSpace(getenv("GROK_CLIPBOARD_NO_DATA_CONTROL")) != "" {
		return DataControlMissing, "disabled by GROK_CLIPBOARD_NO_DATA_CONTROL"
	}
	if strings.TrimSpace(getenv("WAYLAND_DISPLAY")) == "" {
		return DataControlNotApplicable, ""
	}
	path, err := lookPath("wayland-info")
	if err != nil {
		// Without a registry probe tool we cannot claim Missing (would false-alarm).
		return DataControlUnavailable, "wayland-info not found"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := runInfo(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return DataControlUnavailable, "probe timed out"
		}
		return DataControlError, strings.TrimSpace(err.Error())
	}
	text := string(out)
	if bytes.Contains(out, []byte("zwlr_data_control_manager_v1")) ||
		bytes.Contains(out, []byte("ext_data_control_manager_v1")) ||
		strings.Contains(text, "zwlr_data_control") ||
		strings.Contains(text, "ext_data_control") {
		return DataControlAvailable, ""
	}
	return DataControlMissing, ""
}

func waylandDataControlFinding(fact DataControlFact, wlCopyAvailable bool) string {
	if fact != DataControlMissing {
		return ""
	}
	msg := "Clipboard copies may fail if you switch away from this Wayland terminal\n" +
		"    Keep this terminal focused until the copy message appears."
	if !wlCopyAvailable {
		msg += " If your distribution does not use apt, install the `wl-clipboard` package with its package manager.\n" +
			"    → Suggested: `sudo apt install wl-clipboard`"
	} else {
		msg += " If your distribution does not use apt, install the `wl-clipboard` package with its package manager."
	}
	return msg
}
