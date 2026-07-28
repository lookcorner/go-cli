package tui

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
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
// through the kitty graphics protocol, chunked with continuation flags. The
// image is scaled to cols by rows terminal cells when either is positive.
func kittyImageSequence(data []byte, cols, rows int) []byte {
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
		out.WriteString("\x1b_Ga=T,f=100,m=" + strconv.Itoa(more))
		if cols > 0 {
			out.WriteString(",c=" + strconv.Itoa(cols))
		}
		if rows > 0 {
			out.WriteString(",r=" + strconv.Itoa(rows))
		}
		out.WriteString(";")
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

var inlineMetadataPattern = regexp.MustCompile(`^•?\s*-?\s*image/[a-z0-9.+-]+ · \d+x\d+ · \d+ bytes`)

// inlineImageCells converts pixel dimensions to terminal cells using the
// conventional 9x18 cell approximation, bounded to maxRows.
func inlineImageCells(widthPx, heightPx, maxRows int) (cols, rows int) {
	cols, rows = (widthPx+8)/9, (heightPx+17)/18
	cols, rows = max(cols, 1), max(rows, 1)
	if maxRows > 0 {
		rows = min(rows, maxRows)
	}
	return cols, rows
}

// inlineImageBlock renders one image attachment as printable lines: the
// protocol sequence followed by blank lines that move the cursor below the
// image. It returns nil for empty data or an unsupported protocol.
func inlineImageBlock(protocol imageProtocol, image tools.ImageAttachment, maxRows int) []string {
	if len(image.Data) == 0 || !strings.HasPrefix(image.MediaType, "image/") {
		return nil
	}
	cols, rows := inlineImageCells(image.Width, image.Height, maxRows)
	var sequence []byte
	switch protocol {
	case imageProtocolKitty:
		sequence = kittyImageSequence(image.Data, cols, rows)
	case imageProtocolITerm2:
		sequence = itermImageSequence(image.Data, cols, rows)
	case imageProtocolSixel:
		encoded, err := sixelImageSequence(image.Data)
		if err != nil {
			return nil
		}
		sequence = encoded
	default:
		return nil
	}
	lines := []string{string(sequence)}
	for index := 1; index < rows; index++ {
		lines = append(lines, "")
	}
	return lines
}

// injectInlineImages expands image metadata lines with their protocol blocks
// while attachments remain, returning the new lines and the consumed count.
func injectInlineImages(lines []string, images []tools.ImageAttachment, protocol imageProtocol, maxRows int) ([]string, int) {
	if protocol == imageProtocolNone || len(images) == 0 {
		return lines, 0
	}
	consumed := 0
	out := make([]string, 0, len(lines)+len(images))
	for _, line := range lines {
		out = append(out, line)
		if consumed < len(images) && inlineMetadataPattern.MatchString(line) {
			if block := inlineImageBlock(protocol, images[consumed], maxRows); block != nil {
				out = append(out, block...)
				consumed++
			}
		}
	}
	return out, consumed
}

// inlineImagesFor builds the display images for one tool result: kitty
// terminals get a live ID and an upload queued for Unicode placeholder
// rendering, other protocols queue bytes for minimal-mode inline blocks.
func (m *model) inlineImagesFor(attachments []tools.ImageAttachment) []session.DisplayImage {
	images := make([]session.DisplayImage, 0, len(attachments))
	for _, image := range attachments {
		display := session.DisplayImage{MediaType: image.MediaType, Width: image.Width, Height: image.Height, Bytes: len(image.Data)}
		if len(image.Data) > 0 {
			m.rememberOverlayImage(image)
			switch m.inlineProtocol() {
			case imageProtocolKitty:
				m.nextKittyID++
				display.KittyID = m.nextKittyID
				display.Data = image.Data
				cols, rows := inlineImageCells(image.Width, image.Height, 12)
				m.kittyUploads = append(m.kittyUploads, kittyTransmitVirtual(display.KittyID, image.Data, cols, rows))
				m.rememberOverlayImageID(display.KittyID, image)
			case imageProtocolITerm2, imageProtocolSixel:
				if m.minimal {
					m.pendingImages = append(m.pendingImages, image)
					if len(m.pendingImages) > 32 {
						m.pendingImages = m.pendingImages[len(m.pendingImages)-32:]
					}
				}
			}
		}
		images = append(images, display)
	}
	return images
}
