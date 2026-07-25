package tui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestKittyPlaceholderGrid(t *testing.T) {
	t.Parallel()
	grid := kittyPlaceholderGrid(3, 2)
	lines := strings.Split(grid, "\n")
	if len(lines) != 2 {
		t.Fatalf("grid lines=%d", len(lines))
	}
	if lines[0] != "\U0010EEEE\u0305\U0010EEEE\U0010EEEE" {
		t.Fatalf("first row=%q", lines[0])
	}
	if lines[1] != "\U0010EEEE\u030d\U0010EEEE\U0010EEEE" {
		t.Fatalf("second row=%q", lines[1])
	}
	if single := kittyPlaceholderGrid(1, 1); single != "\U0010EEEE\u0305" {
		t.Fatalf("single cell=%q", single)
	}
}

func TestKittyTransmitVirtual(t *testing.T) {
	t.Parallel()
	small := string(kittyTransmitVirtual(42, []byte("png"), 10, 2))
	want := "\x1b_Ga=T,f=100,q=2,i=42,U=1,c=10,r=2,m=0;" + base64.StdEncoding.EncodeToString([]byte("png")) + "\x1b\\"
	if small != want {
		t.Fatalf("small=%q want=%q", small, want)
	}
	large := string(kittyTransmitVirtual(7, make([]byte, 6000), 5, 5))
	if !strings.HasPrefix(large, "\x1b_Ga=T,f=100,q=2,i=7,U=1,c=5,r=5,m=1;") {
		t.Fatalf("first chunk header: %.60q", large)
	}
	if chunks := strings.Count(large, "\x1b\\"); chunks != 2 || !strings.Contains(large, "\x1b\\\x1b_Gm=0;") {
		t.Fatalf("chunking malformed: %d chunks", chunks)
	}
}

func TestGorkImageFenceRendersColoredOpaqueGrid(t *testing.T) {
	t.Parallel()
	fence := "before\n\n```gork-image:42:2:2\n" + kittyPlaceholderGrid(2, 2) + "\n```\n\nafter"
	lines := renderMarkdownTheme(fence, 40, false, themePalette{})
	var colored []string
	for _, line := range lines {
		if strings.Contains(line, "\x1b[38;2;0;0;42m") {
			colored = append(colored, line)
		}
	}
	if len(colored) != 2 || !strings.Contains(colored[0], "\U0010EEEE\u0305") || !strings.HasSuffix(colored[0], "\x1b[39m") {
		t.Fatalf("colored placeholders=%q", colored)
	}
	for _, line := range lines {
		if strings.Contains(line, "gork-image") {
			t.Fatalf("fence infostring leaked: %q", line)
		}
	}
}

func TestInlineImagesForAllocatesKittyState(t *testing.T) {
	m := &model{imageProtocol: imageProtocolKitty, protocolChecked: true}
	attachments := []tools.ImageAttachment{{MediaType: "image/png", Width: 90, Height: 36, Data: []byte("png")}}
	images := m.inlineImagesFor(attachments)
	if len(images) != 1 || images[0].KittyID != 1 || len(images[0].Data) == 0 || images[0].Bytes != 3 {
		t.Fatalf("images=%#v", images)
	}
	if len(m.kittyUploads) != 1 || !strings.Contains(string(m.kittyUploads[0]), "i=1,U=1,c=10,r=2") {
		t.Fatalf("uploads=%q", m.kittyUploads)
	}
	again := m.inlineImagesFor(attachments)
	if again[0].KittyID != 2 {
		t.Fatalf("kitty IDs not monotonic: %d", again[0].KittyID)
	}
}

func TestDisplayImageNeverPersistsLiveFields(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(session.DisplayImage{MediaType: "image/png", Width: 1, Height: 1, Bytes: 3, KittyID: 42, Data: []byte("png")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"42"`) || strings.Contains(string(data), "cG5n") {
		t.Fatalf("live fields leaked into JSON: %s", data)
	}
	var decoded session.DisplayImage
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.KittyID != 0 || decoded.Data != nil {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
}
