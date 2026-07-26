package wrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
