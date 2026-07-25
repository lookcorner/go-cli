package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
)

const maxImageBytes = 20 << 20

var ErrEmpty = errors.New("clipboard has no text or image")
var readPlatformFn = readPlatform
var readPrimaryPlatformFn = readPrimaryPlatform

type Content struct {
	Text      string
	MediaType string
	Data      []byte
}

func Read(ctx context.Context) (Content, error) {
	return read(ctx)
}

func ReadPrimary(ctx context.Context) (string, error) {
	text, err := readPrimaryPlatformFn(ctx)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", ErrEmpty
	}
	return text, nil
}

func read(ctx context.Context) (Content, error) {
	content, err := readPlatformFn(ctx)
	if err != nil {
		return Content{}, err
	}
	if len(content.Data) > 0 {
		if content.MediaType != "image/png" {
			return Content{}, fmt.Errorf("unsupported clipboard image type %q", content.MediaType)
		}
		if len(content.Data) > maxImageBytes {
			return Content{}, fmt.Errorf("clipboard image exceeds %d bytes", maxImageBytes)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(content.Data))
		if err != nil || format != "png" || config.Width < 1 || config.Height < 1 ||
			int64(config.Width)*int64(config.Height) > 100_000_000 {
			return Content{}, errors.New("clipboard image is not valid PNG data")
		}
		return content, nil
	}
	if content.Text == "" {
		return Content{}, ErrEmpty
	}
	return content, nil
}
