package tui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/tools"
)

var kittyPlaceholderFG = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)

type imageOverlayClickEvent struct{ id int }

// kittyIDFromStyledLine extracts a Kitty image id from a placeholder line that
// was colored with kittyPlaceholderColor (truecolor FG encodes the id).
func kittyIDFromStyledLine(line string) (int, bool) {
	if !strings.Contains(line, kittyPlaceholder) {
		return 0, false
	}
	match := kittyPlaceholderFG.FindStringSubmatch(line)
	if len(match) != 4 {
		return 0, false
	}
	r, _ := strconv.Atoi(match[1])
	g, _ := strconv.Atoi(match[2])
	b, _ := strconv.Atoi(match[3])
	id := r<<16 | g<<8 | b
	return id, id > 0
}

func (m *model) rememberOverlayImageID(id int, image tools.ImageAttachment) {
	if m == nil || id < 1 || len(image.Data) == 0 {
		return
	}
	if m.overlayByKittyID == nil {
		m.overlayByKittyID = map[int]tools.ImageAttachment{}
	}
	m.overlayByKittyID[id] = tools.ImageAttachment{
		MediaType: image.MediaType, Width: image.Width, Height: image.Height,
		Data: append([]byte(nil), image.Data...),
	}
	m.rememberOverlayImage(image)
}

func (m *model) openImageOverlayByKittyID(id int) bool {
	if m == nil || id < 1 {
		return false
	}
	image, ok := m.overlayByKittyID[id]
	if !ok {
		return false
	}
	return m.openImageOverlay(image, "Image")
}
