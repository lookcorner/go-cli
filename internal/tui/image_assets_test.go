package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestListSessionImages(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	images := filepath.Join(dir, "artifacts", "s", "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "1.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	list := m.listSessionImages()
	if !strings.Contains(list, "images/1.png") || !strings.Contains(list, "Session images (1)") {
		t.Fatalf("list=%q", list)
	}
}

func TestOpenLatestImageOverlayFallsBackToSessionAsset(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	images := filepath.Join(dir, "artifacts", "s", "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	png := encodeOverlayPNG(t, 2, 2)
	if err := os.WriteFile(filepath.Join(images, "1.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}, imageProtocol: imageProtocolKitty, protocolChecked: true}
	if !m.openLatestImageOverlay() || m.imageOverlay == nil {
		t.Fatalf("overlay not opened status=%q", m.status)
	}
	if m.imageOverlay.title != "images/1.png" {
		t.Fatalf("title=%q", m.imageOverlay.title)
	}
	m.imageOverlay = nil
	if !m.openImageOverlayPath("1.png") || m.imageOverlay == nil || m.imageOverlay.title != "images/1.png" {
		t.Fatalf("basename open status=%q overlay=%v", m.status, m.imageOverlay)
	}
	m.imageOverlay = nil
	if !m.openImageOverlayPath("images/1.png") || m.imageOverlay == nil {
		t.Fatalf("uri open status=%q", m.status)
	}
	if m.openImageOverlayPath("missing.png") {
		t.Fatal("missing accepted")
	}
}
