package session

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveAndReadImageAsset(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	uri, path, err := SaveImageAsset(sessionPath, testPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "assets/image-") || !strings.HasSuffix(uri, ".png") {
		t.Fatalf("uri=%q", uri)
	}
	info, err := os.Stat(path)
	wantMode := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		wantMode = 0o666
	}
	if err != nil || info.Mode().Perm() != wantMode {
		t.Fatalf("asset mode=%v err=%v", info.Mode(), err)
	}
	data, err := ReadAsset(sessionPath, uri, "image/png")
	if err != nil || !bytes.Equal(data, testPNG) {
		t.Fatalf("read back %d bytes err=%v", len(data), err)
	}
	if _, err := ReadAsset(sessionPath, "../escape.png", "image/png"); err == nil {
		t.Fatal("traversal asset accepted")
	}
	if _, err := ReadAsset(sessionPath, uri, "image/gif"); err == nil {
		t.Fatal("mismatched media type accepted")
	}
	if _, _, err := SaveImageAsset(sessionPath, []byte("not an image"), "image/png"); err == nil {
		t.Fatal("invalid image data accepted")
	}
}
