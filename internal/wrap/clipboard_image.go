package wrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"time"

	"golang.org/x/image/draw"

	appclipboard "github.com/lookcorner/go-cli/internal/clipboard"
)

const (
	// RequestOSCBody is the OSC body after ESC ] for a host image request.
	RequestOSCBody = "999;GrokWrapClipboardImage?"
	// MagicIMG prefixes a successful host image bracketed-paste frame.
	MagicIMG = "GROK_WRAP_IMG"
	// MagicNONE means the host has no image (not a prefix of MagicIMG).
	MagicNONE = "GROK_WRAP_NONE"
	// MaxWrapImageBytes caps decoded image payload on this path.
	MaxWrapImageBytes = 20 << 20
)

// WrapImagePaste is the result of decoding a wrap-injected bracketed paste.
type WrapImagePaste struct {
	Image   *appclipboard.Content
	NoImage bool
}

// SinkActive reports whether this process is running under gork wrap's OSC 52 sink.
func SinkActive() bool {
	return os.Getenv("GROK_OSC52_SINK") != "" || os.Getenv("LC_GROK_OSC52_SINK") != ""
}

// RequestOSCBytes is the full request sequence written to stderr by the remote.
func RequestOSCBytes() []byte {
	out := make([]byte, 0, 2+len(RequestOSCBody)+1)
	out = append(out, 0x1b, ']')
	out = append(out, RequestOSCBody...)
	out = append(out, 0x07)
	return out
}

// MaybeRequestHostImage writes the request OSC when the local clipboard miss is
// complete and wrap sink env is active. emit is typically writing to stderr.
func MaybeRequestHostImage(sinkActive bool, localImage bool, localText, localFileURLs string, emit func() error) bool {
	if !sinkActive || localImage || strings.TrimSpace(localText) != "" || strings.TrimSpace(localFileURLs) != "" {
		return false
	}
	return emit() == nil
}

// TryDecodeHostImagePaste decodes wrap host-image paste content.
// nil means not wrap magic (caller treats as normal text). Malformed wrap
// frames yield NoImage so they never land as text.
func TryDecodeHostImagePaste(text string) *WrapImagePaste {
	if text == MagicNONE {
		return &WrapImagePaste{NoImage: true}
	}
	rest, ok := strings.CutPrefix(text, MagicIMG)
	if !ok {
		return nil
	}
	rest, ok = strings.CutPrefix(rest, "\n")
	if !ok {
		return &WrapImagePaste{NoImage: true}
	}
	mime, b64, ok := strings.Cut(rest, "\n")
	if !ok || mime == "" || b64 == "" {
		return &WrapImagePaste{NoImage: true}
	}
	b64 = strings.TrimRight(b64, "\n\r\t ")
	approx := len(b64) * 3 / 4
	if approx > MaxWrapImageBytes {
		return &WrapImagePaste{NoImage: true}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 || len(data) > MaxWrapImageBytes {
		return &WrapImagePaste{NoImage: true}
	}
	return &WrapImagePaste{Image: &appclipboard.Content{MediaType: mime, Data: data}}
}

// EncodeHostImageResponse builds bracketed-paste bytes for wrap to inject into PTY stdin.
// Oversized host images are JPEG-recompressed when possible.
func EncodeHostImageResponse(img *appclipboard.Content) []byte {
	if img == nil || len(img.Data) == 0 {
		return []byte("\x1b[200~" + MagicNONE + "\x1b[201~")
	}
	mime, data, ok := fitImageForWrapBudget(img, MaxWrapImageBytes)
	if !ok {
		return []byte("\x1b[200~" + MagicNONE + "\x1b[201~")
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return []byte("\x1b[200~" + MagicIMG + "\n" + mime + "\n" + b64 + "\x1b[201~")
}

func fitImageForWrap(img *appclipboard.Content) (mime string, data []byte, ok bool) {
	return fitImageForWrapBudget(img, MaxWrapImageBytes)
}

// fitImageForWrapBudget keeps encoded bytes under budget, JPEG-recompressing if needed.
func fitImageForWrapBudget(img *appclipboard.Content, budget int) (mime string, data []byte, ok bool) {
	if len(img.Data) <= budget {
		mime = img.MediaType
		if mime == "" {
			mime = "image/png"
		}
		return mime, img.Data, true
	}
	decoded, _, err := image.Decode(bytes.NewReader(img.Data))
	if err != nil {
		return "", nil, false
	}
	for _, quality := range []int{85, 70, 55, 40, 25} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, decoded, &jpeg.Options{Quality: quality}); err != nil {
			continue
		}
		data := buf.Bytes()
		if len(data) > 0 && len(data) <= budget {
			return "image/jpeg", data, true
		}
	}
	bounds := decoded.Bounds()
	width := max(bounds.Dx()/2, 1)
	height := max(bounds.Dy()/2, 1)
	small := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), decoded, bounds, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 60}); err != nil {
		return "", nil, false
	}
	data = buf.Bytes()
	if len(data) == 0 || len(data) > budget {
		return "", nil, false
	}
	return "image/jpeg", data, true
}

// HostClipboardImageFrame reads the host clipboard and encodes a wrap response frame.
func HostClipboardImageFrame(read func(context.Context) (appclipboard.Content, error)) []byte {
	if read == nil {
		read = appclipboard.ReadWrapSource
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	content, err := read(ctx)
	if err != nil || len(content.Data) == 0 {
		return EncodeHostImageResponse(nil)
	}
	return EncodeHostImageResponse(&content)
}
