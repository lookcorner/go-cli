package tui

import (
	"testing"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestKittyIDFromStyledLine(t *testing.T) {
	line := kittyPlaceholderColor(42) + kittyPlaceholder + kittyPlaceholder + "\x1b[39m"
	id, ok := kittyIDFromStyledLine(line)
	if !ok || id != 42 {
		t.Fatalf("id=%d ok=%v", id, ok)
	}
	if _, ok := kittyIDFromStyledLine("plain text"); ok {
		t.Fatal("plain line should miss")
	}
}

func TestOpenImageOverlayByKittyID(t *testing.T) {
	data := encodeOverlayPNG(t, 16, 8)
	m := &model{width: 60, height: 20, imageProtocol: imageProtocolKitty, protocolChecked: true}
	m.rememberOverlayImageID(7, tools.ImageAttachment{MediaType: "image/png", Width: 16, Height: 8, Data: data})
	if !m.openImageOverlayByKittyID(7) || m.imageOverlay == nil {
		t.Fatal("expected overlay from kitty id")
	}
	if m.openImageOverlayByKittyID(99) {
		t.Fatal("unknown id should fail")
	}
}
