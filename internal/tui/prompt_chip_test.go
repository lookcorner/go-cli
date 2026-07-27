package tui

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
)

func TestPromptImageChipLine(t *testing.T) {
	t.Parallel()
	images := []api.ContentPart{{Type: "input_image"}, {Type: "input_image"}, {Type: "input_image"}}
	line, hits := promptImageChipLine(images, 80)
	if line != "[Image #1] [Image #2] [Image #3]" {
		t.Fatalf("line=%q", line)
	}
	if len(hits) != 3 || hits[0].startCol != 0 || hits[0].endCol != 10 || hits[1].startCol != 11 {
		t.Fatalf("hits=%#v", hits)
	}
	narrow, narrowHits := promptImageChipLine(images, 12)
	if narrow != "[Image #1]" || len(narrowHits) != 1 {
		t.Fatalf("narrow=%q hits=%#v", narrow, narrowHits)
	}
	if empty, hits := promptImageChipLine(nil, 80); empty != "" || hits != nil {
		t.Fatalf("empty=%q hits=%#v", empty, hits)
	}
}

func TestDecodePromptImagePart(t *testing.T) {
	t.Parallel()
	payload := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	image, ok := decodePromptImagePart(api.ContentPart{
		Type:     "input_image",
		ImageURL: "data:image/png;base64," + payload,
	})
	if !ok || image.MediaType != "image/png" || string(image.Data) != "png-bytes" {
		t.Fatalf("got %#v ok=%v", image, ok)
	}
	if _, ok := decodePromptImagePart(api.ContentPart{Type: "input_text", Text: "x"}); ok {
		t.Fatal("expected reject non-image")
	}
	if _, ok := decodePromptImagePart(api.ContentPart{Type: "input_image", ImageURL: "https://example.com/a.png"}); ok {
		t.Fatal("expected reject non-data URL")
	}
}

func TestPromptChipHoverOpensAndClears(t *testing.T) {
	t.Parallel()
	payload := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	m := &model{
		width: 80, height: 24, promptChipHover: -1,
		imageProtocol: imageProtocolKitty, protocolChecked: true,
		promptImages: []api.ContentPart{{
			Type: "input_image", ImageURL: "data:image/png;base64," + payload,
		}},
	}
	if !m.setPromptChipHover(0, false) {
		t.Fatal("expected hover open")
	}
	if m.promptChipHover != 0 || m.imageOverlay == nil || m.imageOverlay.source != promptChipHoverSource {
		t.Fatalf("hover state=%d overlay=%#v", m.promptChipHover, m.imageOverlay)
	}
	if !strings.Contains(m.imageOverlay.title, "Image #1") {
		t.Fatalf("title=%q", m.imageOverlay.title)
	}
	if cmd := m.clearPromptChipHover(); cmd == nil {
		t.Fatal("expected clear cmd for kitty")
	}
	if m.promptChipHover != -1 || m.imageOverlay != nil {
		t.Fatalf("after clear hover=%d overlay=%v", m.promptChipHover, m.imageOverlay)
	}
}

func TestPromptChipClickStickyIgnoresLeave(t *testing.T) {
	t.Parallel()
	payload := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	m := &model{
		width: 80, height: 24, promptChipHover: -1,
		imageProtocol: imageProtocolKitty, protocolChecked: true,
		promptImages: []api.ContentPart{{
			Type: "input_image", ImageURL: "data:image/png;base64," + payload,
		}},
	}
	if !m.setPromptChipHover(0, true) {
		t.Fatal("expected sticky open")
	}
	if m.imageOverlay.source != promptChipClickSource {
		t.Fatalf("source=%q", m.imageOverlay.source)
	}
	if cmd := m.clearPromptChipHover(); cmd != nil || m.imageOverlay == nil {
		t.Fatalf("sticky leave should keep overlay; cmd=%v overlay=%v", cmd, m.imageOverlay)
	}
	if m.promptChipHover != -1 {
		t.Fatalf("hover index should clear; got %d", m.promptChipHover)
	}
}

func TestPromptChipAtHitTesting(t *testing.T) {
	t.Parallel()
	m := &model{promptChipHits: []promptChipHit{{index: 0, startCol: 0, endCol: 10}, {index: 1, startCol: 11, endCol: 21}}}
	if index, ok := m.promptChipAt(3); !ok || index != 0 {
		t.Fatalf("got %d ok=%v", index, ok)
	}
	if index, ok := m.promptChipAt(15); !ok || index != 1 {
		t.Fatalf("got %d ok=%v", index, ok)
	}
	if _, ok := m.promptChipAt(10); ok {
		t.Fatal("expected miss on boundary")
	}
}
