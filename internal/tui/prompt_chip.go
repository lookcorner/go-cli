package tui

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

type promptChipHit struct {
	index    int
	startCol int
	endCol   int
}

const (
	promptChipHoverSource = "prompt-hover"
	promptChipClickSource = "prompt-click"
)

type promptChipHoverEvent struct {
	index  int
	active bool
	sticky bool
}

// promptImageChipLine renders per-image chips and hit boxes for footer hover.
func promptImageChipLine(images []api.ContentPart, width int) (string, []promptChipHit) {
	if len(images) == 0 || width < 8 {
		return "", nil
	}
	var b strings.Builder
	var hits []promptChipHit
	col := 0
	for index := range images {
		label := fmt.Sprintf("[Image #%d]", index+1)
		need := utf8.RuneCountInString(label)
		if index > 0 {
			need++ // space
		}
		if col+need > width {
			break
		}
		if index > 0 {
			b.WriteByte(' ')
			col++
		}
		start := col
		b.WriteString(label)
		col += utf8.RuneCountInString(label)
		hits = append(hits, promptChipHit{index: index, startCol: start, endCol: col})
	}
	if len(hits) == 0 {
		return "", nil
	}
	return b.String(), hits
}

func decodePromptImagePart(part api.ContentPart) (tools.ImageAttachment, bool) {
	if part.Type != "input_image" || !strings.HasPrefix(part.ImageURL, "data:") {
		return tools.ImageAttachment{}, false
	}
	header, payload, ok := strings.Cut(strings.TrimPrefix(part.ImageURL, "data:"), ",")
	if !ok || payload == "" {
		return tools.ImageAttachment{}, false
	}
	mediaType, _, _ := strings.Cut(header, ";")
	if mediaType == "" {
		mediaType = "image/png"
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return tools.ImageAttachment{}, false
	}
	return tools.ImageAttachment{MediaType: mediaType, Data: data}, true
}

func (m *model) promptChipAt(x int) (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, hit := range m.promptChipHits {
		if x >= hit.startCol && x < hit.endCol {
			return hit.index, true
		}
	}
	return 0, false
}

func (m *model) setPromptChipHover(index int, sticky bool) bool {
	if m == nil || index < 0 || index >= len(m.promptImages) {
		return false
	}
	source := promptChipHoverSource
	if sticky {
		source = promptChipClickSource
	}
	if m.promptChipHover == index && m.imageOverlay != nil {
		switch m.imageOverlay.source {
		case promptChipHoverSource:
			if sticky {
				m.imageOverlay.source = promptChipClickSource
				return true
			}
			return false
		case promptChipClickSource:
			return false
		}
	}
	image, ok := decodePromptImagePart(m.promptImages[index])
	if !ok {
		m.status = "prompt image preview unavailable"
		return false
	}
	if !m.openImageOverlay(image, fmt.Sprintf("Image #%d", index+1)) {
		return false
	}
	m.imageOverlay.source = source
	m.promptChipHover = index
	return true
}

func (m *model) clearPromptChipHover() tea.Cmd {
	if m == nil || m.promptChipHover < 0 {
		return nil
	}
	m.promptChipHover = -1
	if m.imageOverlay != nil && m.imageOverlay.source == promptChipHoverSource {
		return m.closeImageOverlay()
	}
	return nil
}
