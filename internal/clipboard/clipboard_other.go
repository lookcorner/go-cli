//go:build !darwin && !linux && !windows

package clipboard

import (
	"context"
	"errors"
)

func readPlatform(context.Context) (Content, error) {
	return Content{}, errors.New("clipboard is unavailable on this platform")
}
