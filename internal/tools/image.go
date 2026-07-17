package tools

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

var _ ResultTool = (*readFileTool)(nil)

func (t *readFileTool) ExecuteResult(ctx context.Context, raw json.RawMessage) (ExecutionResult, error) {
	result, pdfFile, err := t.readPDFImages(ctx, raw)
	if pdfFile || err != nil {
		return result, err
	}
	result, imageFile, err := t.readImage(raw)
	if imageFile || err != nil {
		return result, err
	}
	result.Output, err = t.Execute(ctx, raw)
	return result, err
}

func (t *readFileTool) readPDFImages(ctx context.Context, raw json.RawMessage) (ExecutionResult, bool, error) {
	var args struct {
		TargetFile string `json:"target_file"`
		Path       string `json:"path"`
		Pages      string `json:"pages"`
		Format     string `json:"format"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Format == "text" {
		return ExecutionResult{}, false, nil
	}
	requestedPath := args.TargetFile
	if requestedPath == "" {
		requestedPath = args.Path
	}
	path, err := t.resolvePath(requestedPath)
	if err != nil {
		return ExecutionResult{}, strings.EqualFold(filepath.Ext(requestedPath), ".pdf"), err
	}
	file, err := os.Open(path)
	if err != nil {
		if strings.EqualFold(filepath.Ext(requestedPath), ".pdf") {
			return ExecutionResult{}, true, fmt.Errorf("open %q: %w", requestedPath, err)
		}
		return ExecutionResult{}, false, nil
	}
	defer file.Close()
	header := make([]byte, 5)
	headerBytes, _ := file.Read(header)
	isPDF := strings.EqualFold(filepath.Ext(path), ".pdf") || headerBytes == len(header) && bytes.Equal(header, []byte("%PDF-"))
	if !isPDF {
		return ExecutionResult{}, false, nil
	}
	if args.Format != "" && args.Format != "image" {
		return ExecutionResult{}, true, fmt.Errorf("invalid PDF format %q; supported values are image and text", args.Format)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ExecutionResult{}, true, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPDFBytes+1))
	if err != nil {
		return ExecutionResult{}, true, fmt.Errorf("read PDF %q: %w", requestedPath, err)
	}
	if len(data) > maxPDFBytes {
		return ExecutionResult{}, true, fmt.Errorf("PDF %q exceeds %d bytes", requestedPath, maxPDFBytes)
	}
	pages, total, err := selectPDFPages(data, args.Pages)
	if err != nil {
		return ExecutionResult{}, true, fmt.Errorf("read PDF %q: %w", requestedPath, err)
	}
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ExecutionResult{}, true, errors.New("PDF image output requires pdftoppm (Poppler)")
	}
	tempDir, err := os.MkdirTemp("", "gork-pdf-*")
	if err != nil {
		return ExecutionResult{}, true, err
	}
	defer os.RemoveAll(tempDir)
	renderCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	images := make([]ImageAttachment, 0, len(pages))
	for _, page := range pages {
		prefix := filepath.Join(tempDir, fmt.Sprintf("page-%d", page))
		command := exec.CommandContext(renderCtx, pdftoppm, "-jpeg", "-jpegopt", "quality=85", "-r", "150", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), "-singlefile", path, prefix)
		if output, err := command.CombinedOutput(); err != nil {
			if errors.Is(renderCtx.Err(), context.DeadlineExceeded) {
				return ExecutionResult{}, true, errors.New("PDF rendering timed out after 60 seconds")
			}
			return ExecutionResult{}, true, fmt.Errorf("render PDF page %d: %w: %s", page, err, strings.TrimSpace(string(output)))
		}
		imageData, err := os.ReadFile(prefix + ".jpg")
		if err != nil {
			return ExecutionResult{}, true, fmt.Errorf("read rendered PDF page %d: %w", page, err)
		}
		attachment, err := NewImageAttachment("image/jpeg", imageData)
		if err != nil {
			return ExecutionResult{}, true, fmt.Errorf("decode rendered PDF page %d: %w", page, err)
		}
		images = append(images, attachment)
	}
	return ExecutionResult{
		Output: fmt.Sprintf("[PDF: %s (%d pages rendered, %d total)]", requestedPath, len(images), total),
		Images: images,
	}, true, nil
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
	path, err := t.resolvePath(requestedPath)
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
	attachment, err := NewImageAttachment(mediaType, data)
	if err != nil {
		return ExecutionResult{}, true, fmt.Errorf("decode image %q: %w", requestedPath, err)
	}
	return ExecutionResult{
		Output: fmt.Sprintf("[Image: %s (%s, %dx%d)]", requestedPath, mediaType, attachment.Width, attachment.Height),
		Images: []ImageAttachment{attachment},
	}, true, nil
}

func DecodeImageAttachment(mediaType, data string) (ImageAttachment, error) {
	if base64.StdEncoding.DecodedLen(len(data)) > maxImageBytes {
		return ImageAttachment{}, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ImageAttachment{}, errors.New("image data is not valid base64")
	}
	return NewImageAttachment(mediaType, decoded)
}

func NewImageAttachment(mediaType string, data []byte) (ImageAttachment, error) {
	if len(data) > maxImageBytes {
		return ImageAttachment{}, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	expected := map[string]string{"image/gif": "gif", "image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp"}[mediaType]
	if expected == "" {
		return ImageAttachment{}, fmt.Errorf("unsupported image media type %q", mediaType)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ImageAttachment{}, err
	}
	if format != expected {
		return ImageAttachment{}, fmt.Errorf("image data is %s, not %s", format, expected)
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 100_000_000 {
		return ImageAttachment{}, errors.New("invalid image dimensions")
	}
	return ImageAttachment{MediaType: mediaType, Data: data, Width: config.Width, Height: config.Height}, nil
}
