package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

const maxImageBytes = 20 << 20

var imageTypes = map[string]string{
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

func (t *readFileTool) ExecuteResult(ctx context.Context, raw json.RawMessage) (ExecutionResult, error) {
	result, imageFile, err := t.readImage(raw)
	if imageFile || err != nil {
		return result, err
	}
	result.Output, err = t.Execute(ctx, raw)
	return result, err
}

func (t *readFileTool) readImage(raw json.RawMessage) (ExecutionResult, bool, error) {
	var args struct {
		TargetFile string `json:"target_file"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ExecutionResult{}, false, nil
	}
	requestedPath := args.TargetFile
	if requestedPath == "" {
		requestedPath = args.Path
	}
	expectedType, knownExtension := imageTypes[strings.ToLower(filepath.Ext(requestedPath))]
	path, err := t.ws.Resolve(requestedPath)
	if err != nil {
		return ExecutionResult{}, knownExtension, err
	}
	file, err := os.Open(path)
	if err != nil {
		if knownExtension {
			return ExecutionResult{}, true, fmt.Errorf("open %q: %w", requestedPath, err)
		}
		return ExecutionResult{}, false, nil
	}
	defer file.Close()
	header := make([]byte, 512)
	headerBytes, headerErr := file.Read(header)
	if headerErr != nil && !errors.Is(headerErr, io.EOF) {
		return ExecutionResult{}, knownExtension, fmt.Errorf("read image header %q: %w", requestedPath, headerErr)
	}
	mediaType := http.DetectContentType(header[:headerBytes])
	if mediaType != "image/gif" && mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
		if knownExtension {
			return ExecutionResult{}, true, fmt.Errorf("image %q is not valid %s data", requestedPath, expectedType)
		}
		return ExecutionResult{}, false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ExecutionResult{}, true, fmt.Errorf("seek image %q: %w", requestedPath, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return ExecutionResult{}, knownExtension, fmt.Errorf("read image %q: %w", requestedPath, err)
	}
	if len(data) > maxImageBytes {
		return ExecutionResult{}, true, fmt.Errorf("image %q exceeds %d bytes", requestedPath, maxImageBytes)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 100_000_000 {
		if err == nil {
			err = errors.New("invalid image dimensions")
		}
		return ExecutionResult{}, true, fmt.Errorf("decode image %q: %w", requestedPath, err)
	}
	return ExecutionResult{
		Output: fmt.Sprintf("[Image: %s (%s, %dx%d)]", requestedPath, mediaType, config.Width, config.Height),
		Images: []ImageAttachment{{MediaType: mediaType, Data: data}},
	}, true, nil
}
