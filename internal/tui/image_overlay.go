package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/tools"
)

type imageOverlayState struct {
	title      string
	mediaType  string
	widthPx    int
	heightPx   int
	bytes      int
	data       []byte
	retransmit bool
	placement  OverlayPlacement
	rect       OverlayRect
}

func (m *model) openImageOverlay(image tools.ImageAttachment, title string) bool {
	if m == nil || m.minimal || len(image.Data) == 0 || !strings.HasPrefix(image.MediaType, "image/") {
		return false
	}
	if !OverlaySupportsPixels(m.inlineProtocol()) {
		m.status = "image overlay needs Kitty or iTerm2 graphics"
		return false
	}
	if title == "" {
		title = "Image"
	}
	m.imageOverlay = &imageOverlayState{
		title: title, mediaType: image.MediaType,
		widthPx: image.Width, heightPx: image.Height, bytes: len(image.Data),
		data: append([]byte(nil), image.Data...), retransmit: true,
	}
	m.status = "image preview"
	return true
}

func (m *model) openLatestImageOverlay() bool {
	if m == nil {
		return false
	}
	for index := len(m.overlayImages) - 1; index >= 0; index-- {
		if m.openImageOverlay(m.overlayImages[index], fmt.Sprintf("Image #%d", index+1)) {
			return true
		}
	}
	return false
}

func (m *model) rememberOverlayImage(image tools.ImageAttachment) {
	if m == nil || len(image.Data) == 0 || !strings.HasPrefix(image.MediaType, "image/") {
		return
	}
	m.overlayImages = append(m.overlayImages, tools.ImageAttachment{
		MediaType: image.MediaType, Width: image.Width, Height: image.Height,
		Data: append([]byte(nil), image.Data...),
	})
	if len(m.overlayImages) > 16 {
		m.overlayImages = m.overlayImages[len(m.overlayImages)-16:]
	}
}

func (m *model) closeImageOverlay() tea.Cmd {
	if m == nil || m.imageOverlay == nil {
		return nil
	}
	m.imageOverlay = nil
	if m.running {
		m.status = "thinking"
	} else {
		m.status = "ready"
	}
	protocol := m.inlineProtocol()
	if protocol != imageProtocolKitty {
		return nil
	}
	clear := ClearOverlayKittyImage()
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(clear)
		return nil
	}
}

func (m *model) handleImageOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	stroke := msg.Keystroke()
	if stroke == "ctrl+q" {
		return m, tea.Quit
	}
	if key.Code == tea.KeyEsc || stroke == "q" {
		return m, m.closeImageOverlay()
	}
	return m, nil
}

func (m *model) imageOverlayChrome(visible []string, width, height int) []string {
	if m.imageOverlay == nil || height < 6 || width < 24 {
		return visible
	}
	for len(visible) < height {
		visible = append(visible, "")
	}
	colors := m.colors()
	boxW := min(width-4, max(36, width*3/4))
	boxH := min(height-2, max(10, height*3/4))
	x0 := (width - boxW) / 2
	y0 := (height - boxH) / 2
	rect := OverlayRect{X: x0, Y: y0, Width: boxW, Height: boxH}
	imgW, imgH := m.imageOverlay.widthPx, m.imageOverlay.heightPx
	if imgW < 1 {
		imgW = 640
	}
	if imgH < 1 {
		imgH = 480
	}
	placement, ok := CenteredOverlayPlacement(imgW, imgH, rect)
	if !ok {
		placement = OverlayPlacement{Cols: max(1, boxW-4), Rows: max(1, boxH-4), X: x0 + 2, Y: y0 + 2}
	}
	m.imageOverlay.rect = rect
	m.imageOverlay.placement = placement

	top := boxLine('╭', '─', '╮', boxW)
	bottom := boxLine('╰', '─', '╯', boxW)
	title := truncate(" "+m.imageOverlay.title+" ", boxW-2)
	meta := truncate(fmt.Sprintf(" %s · %dx%d · %d bytes ", m.imageOverlay.mediaType, m.imageOverlay.widthPx, m.imageOverlay.heightPx, m.imageOverlay.bytes), boxW-2)
	hint := truncate(" Esc close ", boxW-2)

	paint := func(y int, content string) {
		if y < 0 || y >= len(visible) {
			return
		}
		line := visible[y]
		plain := stripUIANSI(line)
		if len([]rune(plain)) < width {
			plain += strings.Repeat(" ", width-len([]rune(plain)))
		}
		runes := []rune(plain)
		left := string(runes[:min(len(runes), x0)])
		right := ""
		if x0+boxW < len(runes) {
			right = string(runes[x0+boxW:])
		}
		visible[y] = left + content + right
	}

	paint(y0, ansiBold+colors.modal+padOverlayLine(top, boxW)+ansiReset)
	paint(y0+1, colors.modal+"│"+ansiReset+ansiBold+padOverlayLine(title, boxW-2)+ansiReset+colors.modal+"│"+ansiReset)
	paint(y0+2, colors.modal+"│"+ansiReset+ansiDim+padOverlayLine(meta, boxW-2)+ansiReset+colors.modal+"│"+ansiReset)
	for y := y0 + 3; y < y0+boxH-2; y++ {
		paint(y, colors.modal+"│"+ansiReset+strings.Repeat(" ", boxW-2)+colors.modal+"│"+ansiReset)
	}
	paint(y0+boxH-2, colors.modal+"│"+ansiReset+ansiDim+padOverlayLine(hint, boxW-2)+ansiReset+colors.modal+"│"+ansiReset)
	paint(y0+boxH-1, colors.modal+padOverlayLine(bottom, boxW)+ansiReset)
	return visible
}

func boxLine(left, fill, right rune, width int) string {
	if width < 2 {
		return string(left) + string(right)
	}
	return string(left) + strings.Repeat(string(fill), width-2) + string(right)
}

func padOverlayLine(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func (m *model) flushImageOverlay() tea.Cmd {
	state := m.imageOverlay
	if state == nil || m.minimal {
		return nil
	}
	protocol := m.inlineProtocol()
	if !OverlaySupportsPixels(protocol) {
		return nil
	}
	esc := BuildOverlayImageEscapes(protocol, state.data, state.placement.Cols, state.placement.Rows, state.placement.X, state.placement.Y, state.retransmit)
	if esc == "" {
		return nil
	}
	state.retransmit = false
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(esc)
		return nil
	}
}
