package wrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	appclipboard "github.com/lookcorner/go-cli/internal/clipboard"
)

func TestEncodeDecodeHostImageRoundTrip(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	frame := EncodeHostImageResponse(&appclipboard.Content{MediaType: "image/png", Data: png})
	if !bytes.HasPrefix(frame, []byte("\x1b[200~"+MagicIMG+"\nimage/png\n")) || !bytes.HasSuffix(frame, []byte("\x1b[201~")) {
		t.Fatalf("frame=%q", frame)
	}
	body := string(frame[len("\x1b[200~") : len(frame)-len("\x1b[201~")])
	decoded := TryDecodeHostImagePaste(body)
	if decoded == nil || decoded.NoImage || decoded.Image == nil || !bytes.Equal(decoded.Image.Data, png) {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestEncodeNoneAndDecodeNone(t *testing.T) {
	frame := EncodeHostImageResponse(nil)
	if string(frame) != "\x1b[200~"+MagicNONE+"\x1b[201~" {
		t.Fatalf("frame=%q", frame)
	}
	decoded := TryDecodeHostImagePaste(MagicNONE)
	if decoded == nil || !decoded.NoImage {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestTryDecodeRejectsMalformedAndOversized(t *testing.T) {
	if got := TryDecodeHostImagePaste("plain text"); got != nil {
		t.Fatalf("plain=%#v", got)
	}
	if got := TryDecodeHostImagePaste(MagicIMG); got == nil || !got.NoImage {
		t.Fatalf("bare magic=%#v", got)
	}
	huge := MagicIMG + "\nimage/png\n" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), MaxWrapImageBytes+10))
	if got := TryDecodeHostImagePaste(huge); got == nil || !got.NoImage {
		t.Fatalf("oversized=%#v", got)
	}
}

func TestMaybeRequestHostImageGates(t *testing.T) {
	var wrote bool
	emit := func() error { wrote = true; return nil }
	if MaybeRequestHostImage(false, false, "", "", emit) || wrote {
		t.Fatal("inactive sink should not emit")
	}
	wrote = false
	if MaybeRequestHostImage(true, true, "", "", emit) || wrote {
		t.Fatal("local image should not emit")
	}
	wrote = false
	if MaybeRequestHostImage(true, false, " text ", "", emit) || wrote {
		t.Fatal("local text should not emit")
	}
	wrote = false
	if !MaybeRequestHostImage(true, false, "", "", emit) || !wrote {
		t.Fatal("full miss should emit")
	}
	if MaybeRequestHostImage(true, false, "", "", func() error { return errors.New("fail") }) {
		t.Fatal("emit error should return false")
	}
}

func TestFitImageForWrapRecompressesOversizedPNG(t *testing.T) {
	pngData := noisyTestPNG(t, 256)
	budget := len(pngData) - 1
	if budget < 64 {
		t.Fatal("test PNG unexpectedly tiny")
	}
	mime, data, ok := fitImageForWrapBudget(&appclipboard.Content{MediaType: "image/png", Data: pngData}, budget)
	if !ok || mime != "image/jpeg" || len(data) == 0 || len(data) > budget {
		t.Fatalf("ok=%v mime=%q len=%d budget=%d", ok, mime, len(data), budget)
	}
}

func TestFitImageForWrapPassthroughUnderBudget(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	mime, data, ok := fitImageForWrap(&appclipboard.Content{MediaType: "image/png", Data: png})
	if !ok || mime != "image/png" || !bytes.Equal(data, png) {
		t.Fatalf("mime=%q ok=%v", mime, ok)
	}
}

func noisyTestPNG(t *testing.T, size int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 17), G: uint8(y * 31), B: uint8(x * y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHostClipboardImageFrame(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	frame := HostClipboardImageFrame(func(context.Context) (appclipboard.Content, error) {
		return appclipboard.Content{MediaType: "image/png", Data: png}, nil
	})
	if !strings.Contains(string(frame), MagicIMG) {
		t.Fatalf("frame=%q", frame)
	}
	frame = HostClipboardImageFrame(func(context.Context) (appclipboard.Content, error) {
		return appclipboard.Content{}, appclipboard.ErrEmpty
	})
	if !strings.Contains(string(frame), MagicNONE) {
		t.Fatalf("empty frame=%q", frame)
	}
}

func TestFilterConsumesWrapImageRequest(t *testing.T) {
	var called int
	filter := NewFilter(nil)
	filter.SetImageRequestHandler(func() { called++ })
	output := filter.Feed(RequestOSCBytes())
	if len(output) != 0 || called != 1 {
		t.Fatalf("output=%q called=%d", output, called)
	}
}
