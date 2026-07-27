package tui

import (
	"strings"
	"testing"
)

func TestCenteredOverlayPlacement(t *testing.T) {
	got, ok := CenteredOverlayPlacement(100, 50, OverlayRect{X: 0, Y: 0, Width: 40, Height: 20})
	if !ok || got.Cols < 1 || got.Rows < 1 {
		t.Fatalf("placement=%#v ok=%v", got, ok)
	}
	if got.X < 1 || got.Y < 1 || got.X+got.Cols > 39 || got.Y+got.Rows > 19 {
		t.Fatalf("placement exceeds margin: %#v", got)
	}
	if _, ok := CenteredOverlayPlacement(10, 10, OverlayRect{Width: 2, Height: 2}); ok {
		t.Fatal("tiny overlay should fail")
	}
}

func TestBuildOverlayImageEscapesKittyTransmitAndPlace(t *testing.T) {
	esc := BuildOverlayImageEscapes(imageProtocolKitty, []byte("png-bytes"), 8, 4, 3, 5, true)
	if !strings.HasPrefix(esc, "\x1b[6;4H") {
		t.Fatalf("missing CUP: %q", esc)
	}
	if !strings.Contains(esc, "a=t,") || !strings.Contains(esc, "a=p,") || !strings.Contains(esc, "z=1") {
		t.Fatalf("expected transmit+place: %q", esc)
	}
	placeOnly := BuildOverlayImageEscapes(imageProtocolKitty, nil, 8, 4, 3, 5, false)
	if strings.Contains(placeOnly, "a=t,") || !strings.Contains(placeOnly, "a=p,") {
		t.Fatalf("place-only=%q", placeOnly)
	}
}

func TestBuildOverlayImageEscapesITermAndUnsupported(t *testing.T) {
	esc := BuildOverlayImageEscapes(imageProtocolITerm2, []byte("png"), 4, 2, 1, 1, true)
	if !strings.Contains(esc, "1337;File=inline=1") {
		t.Fatalf("iterm=%q", esc)
	}
	if BuildOverlayImageEscapes(imageProtocolSixel, []byte("x"), 4, 2, 0, 0, true) != "" {
		t.Fatal("sixel should not drive modal overlays")
	}
	if BuildOverlayImageEscapes(imageProtocolNone, []byte("x"), 4, 2, 0, 0, true) != "" {
		t.Fatal("none should be empty")
	}
	if !OverlaySupportsPixels(imageProtocolKitty) || OverlaySupportsPixels(imageProtocolSixel) {
		t.Fatal("pixel support matrix mismatch")
	}
	if !strings.Contains(ClearOverlayKittyImage(), "a=d,d=i,i=1") {
		t.Fatalf("clear=%q", ClearOverlayKittyImage())
	}
}
