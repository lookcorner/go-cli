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
	// 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
		0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
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
}
