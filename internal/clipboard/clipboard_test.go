package clipboard

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestReadValidatesClipboardContent(t *testing.T) {
	original := readPlatformFn
	defer func() { readPlatformFn = original }()

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		content Content
		err     error
		wantErr string
	}{
		{name: "text", content: Content{Text: "hello"}},
		{name: "image", content: Content{MediaType: "image/png", Data: encoded.Bytes()}},
		{name: "empty", wantErr: ErrEmpty.Error()},
		{name: "platform error", err: context.DeadlineExceeded, wantErr: context.DeadlineExceeded.Error()},
		{name: "unsupported image", content: Content{MediaType: "image/jpeg", Data: encoded.Bytes()}, wantErr: "unsupported clipboard image type"},
		{name: "invalid image", content: Content{MediaType: "image/png", Data: []byte("bad")}, wantErr: "not valid PNG"},
		{name: "oversized image", content: Content{MediaType: "image/png", Data: make([]byte, maxImageBytes+1)}, wantErr: "clipboard image exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			readPlatformFn = func(context.Context) (Content, error) { return test.content, test.err }
			got, err := read(context.Background())
			if test.wantErr == "" {
				if err != nil || got.Text != test.content.Text || got.MediaType != test.content.MediaType {
					t.Fatalf("content=%#v err=%v", got, err)
				}
			} else if err == nil || !errors.Is(err, ErrEmpty) && !errors.Is(err, test.err) && !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
