package terminaldiag

import (
	"errors"
	"strings"
	"testing"
)

func TestFFmpegFindingAndFact(t *testing.T) {
	if finding := ffmpegFinding(true); finding != "" {
		t.Fatalf("available finding=%q", finding)
	}
	missing := ffmpegFinding(false)
	for _, want := range []string{"/play-video", "ffmpeg", "PATH"} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing %q in %q", want, missing)
		}
	}

	lookMissing := func(string) (string, error) { return "", errors.New("missing") }
	snapshot := BuildSnapshot(func(string) string { return "" }, lookMissing, "darwin")
	if snapshot.Facts.FFmpeg {
		t.Fatal("expected ffmpeg fact false")
	}
	joined := strings.Join(snapshot.Findings, "\n")
	if !strings.Contains(joined, "/play-video") {
		t.Fatalf("findings=%q", joined)
	}
	human := snapshot.Human()
	if !strings.Contains(human, "ffmpeg       off") {
		t.Fatalf("human=%s", human)
	}

	lookOK := func(name string) (string, error) {
		if name == "ffmpeg" {
			return "/usr/bin/ffmpeg", nil
		}
		return "", errors.New("missing")
	}
	snapshot = BuildSnapshot(func(string) string {
		return ""
	}, lookOK, "darwin")
	if !snapshot.Facts.FFmpeg {
		t.Fatal("expected ffmpeg fact true")
	}
	if strings.Contains(strings.Join(snapshot.Findings, "\n"), "/play-video") {
		t.Fatalf("unexpected finding: %#v", snapshot.Findings)
	}
}
