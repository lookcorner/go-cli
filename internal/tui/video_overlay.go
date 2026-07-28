package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/session"
)

type videoOverlayTickEvent struct{ epoch uint64 }

func (m *model) openVideoOverlay(viewer *VideoViewer) bool {
	if m == nil || m.minimal || viewer == nil || len(viewer.Frames) == 0 {
		return false
	}
	if !OverlaySupportsPixels(m.inlineProtocol()) {
		m.status = "video overlay needs Kitty or iTerm2 graphics"
		return false
	}
	m.videoOverlay = viewer
	m.videoOverlayEpoch++
	m.status = "video preview"
	return true
}

func (m *model) openVideoOverlayPath(path string) bool {
	resolved, err := m.resolvePlayVideoPath(path)
	if err != nil {
		m.status = err.Error()
		return false
	}
	viewer, err := OpenVideoFromPath(resolved, m.inlineProtocol())
	if err != nil {
		m.status = err.Error()
		return false
	}
	return m.openVideoOverlay(viewer)
}

// resolvePlayVideoPath accepts absolute paths, videos/<name>, bare basenames
// under the session videos/ folder, or "" for the newest session clip.
func (m *model) resolvePlayVideoPath(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	sessionPath := ""
	if m != nil && m.runner != nil {
		sessionPath = strings.TrimSpace(m.runner.SessionPath)
	}
	if arg == "" {
		if sessionPath == "" {
			return "", fmt.Errorf("usage: /play-video <path>")
		}
		asset, ok, err := session.LatestVideoAsset(sessionPath)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no session videos under videos/")
		}
		return asset.Path, nil
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	if sessionPath != "" {
		if resolved, err := session.ResolveVideoAsset(sessionPath, arg); err == nil {
			return resolved, nil
		}
	}
	// Fall back to workspace-relative paths for ad-hoc clips.
	if abs, err := filepath.Abs(arg); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	if sessionPath != "" {
		return "", fmt.Errorf("session video %q not found", arg)
	}
	return "", fmt.Errorf("video path not found: %s", arg)
}

func (m *model) closeVideoOverlay() tea.Cmd {
	if m == nil || m.videoOverlay == nil {
		return nil
	}
	m.videoOverlay = nil
	if m.running {
		m.status = "thinking"
	} else {
		m.status = "ready"
	}
	if m.inlineProtocol() != imageProtocolKitty {
		return nil
	}
	clear := ClearOverlayKittyImage()
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(clear)
		return nil
	}
}

func (m *model) handleVideoOverlayKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	stroke := msg.Keystroke()
	if stroke == "ctrl+q" {
		return m, tea.Quit
	}
	switch {
	case key.Code == tea.KeyEsc || stroke == "q":
		return m, m.closeVideoOverlay()
	case stroke == "space":
		m.videoOverlay.TogglePlayPause()
	case stroke == "left" || stroke == "h":
		m.videoOverlay.SeekBackward()
	case stroke == "right" || stroke == "l":
		m.videoOverlay.SeekForward()
	}
	return m, m.videoOverlayTickCmd()
}

func (m *model) videoOverlayTickCmd() tea.Cmd {
	if m == nil || m.videoOverlay == nil || !m.videoOverlay.Playing {
		return nil
	}
	epoch := m.videoOverlayEpoch
	delay := time.Duration(float64(time.Second) / max(m.videoOverlay.FPS, 1))
	return tea.Tick(delay, func(time.Time) tea.Msg { return videoOverlayTickEvent{epoch: epoch} })
}

func (m *model) videoOverlayChrome(visible []string, width, height int) []string {
	viewer := m.videoOverlay
	if viewer == nil || height < 8 || width < 24 {
		return visible
	}
	for len(visible) < height {
		visible = append(visible, "")
	}
	colors := m.colors()
	boxW := min(width-2, max(28, width*9/10))
	boxH := min(height-1, max(8, height*9/10))
	x0 := (width - boxW) / 2
	y0 := (height - boxH) / 2
	rect := OverlayRect{X: x0, Y: y0, Width: boxW, Height: boxH}
	imgW, imgH := viewer.Width, viewer.Height
	if imgW < 1 {
		imgW = 640
	}
	if imgH < 1 {
		imgH = 360
	}
	placement, ok := CenteredOverlayPlacement(imgW, imgH, OverlayRect{X: x0, Y: y0 + 1, Width: boxW, Height: boxH - 2})
	if !ok {
		placement = OverlayPlacement{Cols: max(1, boxW-4), Rows: max(1, boxH-4), X: x0 + 2, Y: y0 + 2}
	}
	viewer.placementCache = placement
	viewer.rectCache = rect

	title := truncate(fmt.Sprintf(" %s (%dx%d) ", viewer.Title, viewer.Width, viewer.Height), boxW-2)
	progress := truncate(" "+renderVideoProgress(boxW-4, viewer.Progress())+fmt.Sprintf(" %.1fs ", viewer.PositionSecs()), boxW-2)
	hint := truncate(" Space · ←/→ · Esc ", boxW-2)
	top := boxLine('╭', '─', '╮', boxW)
	bottom := boxLine('╰', '─', '╯', boxW)

	paint := func(y int, content string) {
		if y < 0 || y >= len(visible) {
			return
		}
		plain := stripUIANSI(visible[y])
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
	for y := y0 + 2; y < y0+boxH-3; y++ {
		paint(y, colors.modal+"│"+ansiReset+strings.Repeat(" ", boxW-2)+colors.modal+"│"+ansiReset)
	}
	paint(y0+boxH-3, colors.modal+"│"+ansiReset+ansiDim+padOverlayLine(progress, boxW-2)+ansiReset+colors.modal+"│"+ansiReset)
	paint(y0+boxH-2, colors.modal+"│"+ansiReset+ansiDim+padOverlayLine(hint, boxW-2)+ansiReset+colors.modal+"│"+ansiReset)
	paint(y0+boxH-1, colors.modal+padOverlayLine(bottom, boxW)+ansiReset)
	return visible
}

func renderVideoProgress(width int, fraction float64) string {
	if width < 8 {
		return ""
	}
	barW := min(24, width-6)
	filled := int(fraction * float64(barW))
	if filled > barW {
		filled = barW
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", barW-filled) + "]"
}

func (m *model) flushVideoOverlay() tea.Cmd {
	viewer := m.videoOverlay
	if viewer == nil || m.minimal {
		return nil
	}
	protocol := m.inlineProtocol()
	if !OverlaySupportsPixels(protocol) {
		return nil
	}
	frame := viewer.CurrentFrame()
	if len(frame) == 0 {
		return nil
	}
	placement := viewer.placementCache
	if placement.Cols < 1 || placement.Rows < 1 {
		return nil
	}
	esc := BuildOverlayImageEscapes(protocol, frame, placement.Cols, placement.Rows, placement.X, placement.Y, viewer.retransmit)
	if esc == "" {
		return nil
	}
	viewer.retransmit = false
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString(esc)
		return nil
	}
}
