package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestVideoViewerStubPlayback(t *testing.T) {
	frames := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	viewer := NewVideoViewerStub(frames, 32, 16, "clip.mp4")
	viewer.FPS = 100
	viewer.lastFrame = time.Now().Add(-time.Second)
	if !viewer.Tick() || viewer.Current != 1 {
		t.Fatalf("tick current=%d", viewer.Current)
	}
	viewer.FPS = 1
	viewer.SeekForward()
	if viewer.Current != 2 {
		t.Fatalf("seek forward=%d", viewer.Current)
	}
	viewer.SeekBackward()
	if viewer.Current != 1 {
		t.Fatalf("seek back=%d", viewer.Current)
	}
	viewer.TogglePlayPause()
	if viewer.Playing {
		t.Fatal("expected paused")
	}
}

func TestVideoOverlayChromeAndKeys(t *testing.T) {
	data := encodeOverlayPNG(t, 24, 12)
	viewer := NewVideoViewerStub([][]byte{data, data}, 24, 12, "demo.mp4")
	m := &model{width: 80, height: 24, imageProtocol: imageProtocolKitty, protocolChecked: true}
	if !m.openVideoOverlay(viewer) {
		t.Fatal("open failed")
	}
	visible := make([]string, m.contentHeight())
	for i := range visible {
		visible[i] = strings.Repeat(" ", m.width)
	}
	painted := m.videoOverlayChrome(visible, m.width, m.contentHeight())
	plain := stripUIANSI(strings.Join(painted, "\n"))
	if !strings.Contains(plain, "demo.mp4") || !strings.Contains(plain, "Esc") {
		t.Fatalf("chrome=%q", plain)
	}
	if m.flushVideoOverlay() == nil {
		t.Fatal("expected flush")
	}
	next, cmd := m.handleVideoOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if next.(*model).videoOverlay != nil || cmd == nil {
		t.Fatalf("close state=%v cmd=%v", next.(*model).videoOverlay != nil, cmd != nil)
	}
}

func TestOpenVideoFromPathRequiresFFmpegOrGraphics(t *testing.T) {
	if _, err := OpenVideoFromPath("/tmp/missing.mp4", imageProtocolNone); err == nil {
		t.Fatal("expected graphics error")
	}
	if FFmpegAvailable() {
		t.Skip("ffmpeg present; skip missing-binary assertion")
	}
	if _, err := OpenVideoFromPath("/tmp/missing.mp4", imageProtocolKitty); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolvePlayVideoPathSessionAssets(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	videos := filepath.Join(dir, "artifacts", "s", "videos")
	if err := os.MkdirAll(videos, 0o700); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(videos, "3.mp4")
	if err := os.WriteFile(clip, []byte("clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	got, err := m.resolvePlayVideoPath("")
	if err != nil || got != clip {
		t.Fatalf("latest got=%q err=%v", got, err)
	}
	got, err = m.resolvePlayVideoPath("3.mp4")
	if err != nil || got != clip {
		t.Fatalf("basename got=%q err=%v", got, err)
	}
	got, err = m.resolvePlayVideoPath("videos/3.mp4")
	if err != nil || got != clip {
		t.Fatalf("uri got=%q err=%v", got, err)
	}
	if _, err := m.resolvePlayVideoPath("missing.mp4"); err == nil {
		t.Fatal("missing accepted")
	}
	list := m.listSessionVideos()
	if !strings.Contains(list, "videos/3.mp4") || !strings.Contains(list, "Session videos (1)") {
		t.Fatalf("list=%q", list)
	}
}
