package tui

import (
	"image"
	"image/color"
	"image/png"
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/tools"
)

func encodeOverlayPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 40, B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageOverlayChromeAndKeys(t *testing.T) {
	data := encodeOverlayPNG(t, 40, 20)
	m := &model{width: 80, height: 24, imageProtocol: imageProtocolKitty, protocolChecked: true}
	if !m.openImageOverlay(tools.ImageAttachment{MediaType: "image/png", Width: 40, Height: 20, Data: data}, "Image #1") {
		t.Fatal("expected overlay to open")
	}
	visible := make([]string, m.contentHeight())
	for i := range visible {
		visible[i] = strings.Repeat(" ", m.width)
	}
	painted := m.imageOverlayChrome(visible, m.width, m.contentHeight())
	joined := strings.Join(painted, "\n")
	if !strings.Contains(stripUIANSI(joined), "Image #1") || !strings.Contains(stripUIANSI(joined), "Esc close") {
		t.Fatalf("chrome=%q", stripUIANSI(joined))
	}
	if m.imageOverlay.placement.Cols < 1 || m.imageOverlay.placement.Rows < 1 {
		t.Fatalf("placement=%#v", m.imageOverlay.placement)
	}
	cmd := m.flushImageOverlay()
	if cmd == nil {
		t.Fatal("expected flush cmd")
	}
	if m.imageOverlay.retransmit {
		t.Fatal("retransmit should clear after flush queue")
	}

	next, closeCmd := m.handleImageOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if next.(*model).imageOverlay != nil {
		t.Fatal("esc should close overlay")
	}
	if closeCmd == nil {
		t.Fatal("expected clear cmd on kitty")
	}
}

func TestOpenLatestImageOverlayRemembersAttachments(t *testing.T) {
	m := &model{width: 60, height: 20, imageProtocol: imageProtocolITerm2, protocolChecked: true}
	m.rememberOverlayImage(tools.ImageAttachment{MediaType: "image/png", Width: 8, Height: 8, Data: []byte("png")})
	if !m.openLatestImageOverlay() || m.imageOverlay == nil {
		t.Fatal("expected latest overlay")
	}
	minimal := &model{minimal: true, imageProtocol: imageProtocolKitty, protocolChecked: true}
	minimal.rememberOverlayImage(tools.ImageAttachment{MediaType: "image/png", Width: 8, Height: 8, Data: []byte("png")})
	if minimal.openLatestImageOverlay() {
		t.Fatal("minimal mode should refuse overlay")
	}
}
