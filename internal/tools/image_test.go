package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func writePNG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileReturnsValidatedImageAttachment(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "screen.png"))
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	result, err := registry.ExecuteResult(context.Background(), "read_file", json.RawMessage(`{"target_file":"screen.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "[Image: screen.png (image/png, 2x3)]" || len(result.Images) != 1 || result.Images[0].MediaType != "image/png" {
		t.Fatalf("unexpected image result: %#v", result)
	}
	if len(result.Images[0].Data) == 0 {
		t.Fatal("image attachment was empty")
	}
}

func TestReadFileRejectsInvalidImageData(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.png"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if _, err := registry.ExecuteResult(context.Background(), "read_file", json.RawMessage(`{"target_file":"broken.png"}`)); err == nil || !strings.Contains(err.Error(), "not valid image/png") {
		t.Fatalf("invalid image was not rejected: %v", err)
	}
}

func TestReadFileAcceptsWebP(t *testing.T) {
	root := t.TempDir()
	data, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.webp"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	result, err := registry.ExecuteResult(context.Background(), "read_file", json.RawMessage(`{"target_file":"image.webp"}`))
	if err != nil || len(result.Images) != 1 || result.Images[0].MediaType != "image/webp" {
		t.Fatalf("unexpected WebP result=%#v err=%v", result, err)
	}
}
