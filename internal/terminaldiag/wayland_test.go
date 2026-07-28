package terminaldiag

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProbeWaylandDataControlShapes(t *testing.T) {
	env := map[string]string{}
	look := func(string) (string, error) { return "", errors.New("missing") }
	fact, _ := probeWaylandDataControlWith(func(k string) string { return env[k] }, look, nil)
	if fact != DataControlNotApplicable {
		t.Fatalf("no wayland: %s", fact)
	}

	env["WAYLAND_DISPLAY"] = "wayland-0"
	fact, detail := probeWaylandDataControlWith(func(k string) string { return env[k] }, look, nil)
	if fact != DataControlUnavailable || !strings.Contains(detail, "wayland-info") {
		t.Fatalf("missing tool: %s %s", fact, detail)
	}

	look = func(string) (string, error) { return "/usr/bin/wayland-info", nil }
	run := func(context.Context, string) ([]byte, error) {
		return []byte("interface: 'wl_compositor'\n"), nil
	}
	fact, _ = probeWaylandDataControlWith(func(k string) string { return env[k] }, look, run)
	if fact != DataControlMissing {
		t.Fatalf("missing protocol: %s", fact)
	}

	run = func(context.Context, string) ([]byte, error) {
		return []byte("interface: 'zwlr_data_control_manager_v1', version: 2\n"), nil
	}
	fact, _ = probeWaylandDataControlWith(func(k string) string { return env[k] }, look, run)
	if fact != DataControlAvailable {
		t.Fatalf("present: %s", fact)
	}

	env["GROK_CLIPBOARD_NO_DATA_CONTROL"] = "1"
	fact, _ = probeWaylandDataControlWith(func(k string) string { return env[k] }, look, run)
	if fact != DataControlMissing {
		t.Fatalf("kill switch: %s", fact)
	}
}

func TestWaylandDataControlFinding(t *testing.T) {
	if waylandDataControlFinding(DataControlAvailable, true) != "" {
		t.Fatal("available should be quiet")
	}
	if waylandDataControlFinding(DataControlNotApplicable, false) != "" {
		t.Fatal("n/a should be quiet")
	}
	msg := waylandDataControlFinding(DataControlMissing, false)
	if !strings.Contains(msg, "switch away") || !strings.Contains(msg, "wl-clipboard") {
		t.Fatalf("msg=%q", msg)
	}
}

func TestBuildSnapshotIncludesWaylandDataControl(t *testing.T) {
	probeWaylandDataControl = func(func(string) string, func(string) (string, error)) (DataControlFact, string) {
		return DataControlMissing, ""
	}
	t.Cleanup(func() {
		probeWaylandDataControl = func(func(string) string, func(string) (string, error)) (DataControlFact, string) {
			return DataControlNotApplicable, ""
		}
	})
	snapshot := BuildSnapshot(func(string) string { return "" }, func(string) (string, error) {
		return "", errors.New("missing")
	}, "linux")
	if snapshot.Facts.DataControl != string(DataControlMissing) {
		t.Fatalf("fact=%q", snapshot.Facts.DataControl)
	}
	joined := strings.Join(snapshot.Findings, "\n")
	if !strings.Contains(joined, "switch away from this Wayland terminal") {
		t.Fatalf("findings=%v", snapshot.Findings)
	}
	report := snapshot.Human()
	if !strings.Contains(report, "data-control off") {
		t.Fatalf("report=%q", report)
	}
}
