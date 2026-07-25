package tui

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// imageProtocol names a terminal inline-image transport. imageProtocolNone
// means the terminal cannot display pixels and images stay metadata-only.
type imageProtocol uint8

const (
	imageProtocolNone imageProtocol = iota
	imageProtocolKitty
	imageProtocolITerm2
	imageProtocolSixel
)

// detectImageProtocol reports the terminal's inline-image transport from its
// environment. Kitty is preferred where several transports exist.
func detectImageProtocol(env func(string) string) imageProtocol {
	term := strings.ToLower(env("TERM"))
	termProgram := strings.ToLower(env("TERM_PROGRAM"))
	switch {
	case env("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty"):
		return imageProtocolKitty
	case env("GHOSTTY_RESOURCES_DIR") != "" || env("WEZTERM_EXECUTABLE") != "":
		return imageProtocolKitty
	case env("ITERM_SESSION_ID") != "" || termProgram == "iterm.app":
		return imageProtocolITerm2
	case env("WT_SESSION") != "" || strings.Contains(term, "sixel"):
		return imageProtocolSixel
	case termProgram == "mintty":
		return imageProtocolITerm2
	}
	return imageProtocolNone
}

// kittyImageSequence emits image bytes (PNG or any terminal-decodable format)
// through the kitty graphics protocol, chunked with continuation flags.
func kittyImageSequence(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
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
		out.WriteString("\x1b_Ga=T,f=100,m=" + strconv.Itoa(more) + ";")
		out.WriteString(chunk)
		out.WriteString("\x1b\\")
	}
	return []byte(out.String())
}

// itermImageSequence emits image bytes through the iTerm2 inline-images
// protocol with aspect-preserving optional bounds in terminal cells.
func itermImageSequence(data []byte, widthCells, heightCells int) []byte {
	var out strings.Builder
	out.WriteString("\x1b]1337;File=inline=1;preserveAspectRatio=1")
	if widthCells > 0 {
		out.WriteString(";width=" + strconv.Itoa(widthCells))
	}
	if heightCells > 0 {
		out.WriteString(";height=" + strconv.Itoa(heightCells))
	}
	out.WriteString(":")
	out.WriteString(base64.StdEncoding.EncodeToString(data))
	out.WriteString("\a")
	return []byte(out.String())
}
