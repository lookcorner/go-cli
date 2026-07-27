package tui

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// overlayKittyImageID is the shared Kitty placement id for modal overlays
// (matches Rust KITTY_PLACEMENT_ID).
const overlayKittyImageID = 1

// OverlayRect is a cell-space rectangle for modal image placement.
type OverlayRect struct {
	X, Y, Width, Height int
}

// OverlayPlacement is a centered image cell region inside an overlay rect.
type OverlayPlacement struct {
	Cols, Rows int
	X, Y       int
}

// CenteredOverlayPlacement fits an image into overlay with a 1-cell margin,
// matching Rust overlay::centered_placement.
func CenteredOverlayPlacement(imgW, imgH int, overlay OverlayRect) (OverlayPlacement, bool) {
	maxCols := overlay.Width - 2
	maxRows := overlay.Height - 2
	if maxCols < 1 || maxRows < 1 || imgW < 1 || imgH < 1 {
		return OverlayPlacement{}, false
	}
	cols, rows := inlineImageCells(imgW, imgH, maxRows)
	if cols > maxCols {
		scale := float64(maxCols) / float64(cols)
		cols = maxCols
		rows = max(1, int(float64(rows)*scale+0.5))
		if rows > maxRows {
			rows = maxRows
		}
	}
	x := overlay.X + 1 + (maxCols-cols)/2
	y := overlay.Y + 1 + (maxRows-rows)/2
	return OverlayPlacement{Cols: cols, Rows: rows, X: x, Y: y}, true
}

// OverlaySupportsPixels reports whether protocol can paint modal overlays.
// Sixel stays scrollback-inline only (Rust gates iTerm for overlays; Go allows
// iTerm2 for non-Kitty fullscreen pixel overlays).
func OverlaySupportsPixels(protocol imageProtocol) bool {
	return protocol == imageProtocolKitty || protocol == imageProtocolITerm2
}

// BuildOverlayImageEscapes emits cursor-move + protocol bytes for a modal
// overlay image. When retransmit is false, Kitty emits place-only; iTerm2 is a
// no-op (caller keeps prior frame). Returns empty when unsupported.
func BuildOverlayImageEscapes(protocol imageProtocol, data []byte, cols, rows, cellX, cellY int, retransmit bool) string {
	if !OverlaySupportsPixels(protocol) || cols < 1 || rows < 1 || cellX < 0 || cellY < 0 {
		return ""
	}
	var out strings.Builder
	// ANSI CUP is 1-based.
	fmt.Fprintf(&out, "\x1b[%d;%dH", cellY+1, cellX+1)
	switch protocol {
	case imageProtocolKitty:
		if retransmit {
			if len(data) == 0 {
				return ""
			}
			out.Write(kittyTransmitOverlay(overlayKittyImageID, data))
		}
		out.Write(kittyPlaceOverlay(overlayKittyImageID, cols, rows, 1))
	case imageProtocolITerm2:
		if !retransmit || len(data) == 0 {
			return out.String() // cursor only; no re-upload
		}
		out.Write(itermImageSequence(data, cols, rows))
	default:
		return ""
	}
	return out.String()
}

// ClearOverlayKittyImage deletes the shared overlay placement.
func ClearOverlayKittyImage() string {
	return "\x1b_Ga=d,d=i,i=" + strconv.Itoa(overlayKittyImageID) + "\x1b\\"
}

func kittyTransmitOverlay(id int, data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	first := true
	for len(encoded) > 0 {
		chunk := encoded
		if len(chunk) > 4096 {
			chunk = encoded[:4096]
		}
		encoded = encoded[len(chunk):]
		more := 0
		if len(encoded) > 0 {
			more = 1
		}
		if first {
			// a=t transmit-only (not a=T display); placement is separate.
			fmt.Fprintf(&out, "\x1b_Ga=t,f=100,q=2,i=%d,m=%d;", id, more)
			first = false
		} else {
			out.WriteString("\x1b_Gm=" + strconv.Itoa(more) + ";")
		}
		out.WriteString(chunk)
		out.WriteString("\x1b\\")
	}
	return []byte(out.String())
}

func kittyPlaceOverlay(id, cols, rows, z int) []byte {
	return []byte(fmt.Sprintf("\x1b_Ga=p,i=%d,c=%d,r=%d,z=%d\x1b\\", id, cols, rows, z))
}
