package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/tools"
)

func encodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSixelImageSequence(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(0, 1, color.RGBA{255, 0, 0, 255})
	img.Set(1, 0, color.RGBA{0, 0, 255, 255})
	img.Set(1, 1, color.RGBA{0, 0, 255, 255})
	sequence, err := sixelImageSequence(encodeTestPNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1bPq\"1;1;2;2" + "#5;2;0;0;100" + "#180;2;100;0;0" + "#5?B" + "$" + "#180B?" + "\x1b\\"
	if string(sequence) != want {
		t.Fatalf("sequence=%q want=%q", sequence, want)
	}
}

func TestSixelImageSequenceSkipsAlphaAndCompressesRuns(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 5, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(1, 0, color.RGBA{255, 0, 0, 255})
	img.Set(2, 0, color.RGBA{255, 0, 0, 0})
	img.Set(3, 0, color.RGBA{255, 0, 0, 255})
	img.Set(4, 0, color.RGBA{255, 0, 0, 255})
	sequence, err := sixelImageSequence(encodeTestPNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sequence), "#180@@?@@") {
		t.Fatalf("alpha pixel was not skipped: %q", sequence)
	}
	solid := image.NewRGBA(image.Rect(0, 0, 4, 1))
	for x := 0; x < 4; x++ {
		solid.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	sequence, err = sixelImageSequence(encodeTestPNG(t, solid))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sequence), "#180!4@") {
		t.Fatalf("run compression missing: %q", sequence)
	}
}

func TestSixelImageSequenceRejectsInvalidAndOversizedInput(t *testing.T) {
	t.Parallel()
	if _, err := sixelImageSequence([]byte("not an image")); err == nil {
		t.Fatal("invalid input accepted")
	}
	img := image.NewRGBA(image.Rect(0, 0, sixelMaxDimension+1, 1))
	if _, err := sixelImageSequence(encodeTestPNG(t, img)); err == nil {
		t.Fatal("oversized image accepted")
	}
}

func TestInlineImageBlockSixel(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 9, 18))
	attachment := tools.ImageAttachment{MediaType: "image/png", Width: 9, Height: 18, Data: encodeTestPNG(t, img)}
	block := inlineImageBlock(imageProtocolSixel, attachment, 12)
	if block == nil || !strings.HasPrefix(block[0], "\x1bPq\"1;1;9;18") {
		t.Fatalf("sixel block=%q", block)
	}
}
