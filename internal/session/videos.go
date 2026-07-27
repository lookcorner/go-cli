package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VideoAsset is a discovered clip under the session artifact videos/ tree.
type VideoAsset struct {
	Relative string
	Path     string
	Size     int64
	ModTime  time.Time
}

var videoAssetExtensions = map[string]bool{
	".mp4":  true,
	".webm": true,
	".mov":  true,
	".mkv":  true,
}

// ListVideoAssets returns generated/downloaded session videos, newest first.
func ListVideoAssets(sessionPath string) ([]VideoAsset, error) {
	root, err := videoAssetRoot(sessionPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("session videos must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var assets []VideoAsset
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !videoAssetExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		path := filepath.Join(root, name)
		stat, err := os.Lstat(path)
		if err != nil || stat.Mode()&os.ModeSymlink != 0 || !stat.Mode().IsRegular() {
			continue
		}
		assets = append(assets, VideoAsset{
			Relative: filepath.ToSlash(filepath.Join("videos", name)),
			Path:     path,
			Size:     stat.Size(),
			ModTime:  stat.ModTime(),
		})
	}
	sort.SliceStable(assets, func(i, j int) bool {
		if !assets[i].ModTime.Equal(assets[j].ModTime) {
			return assets[i].ModTime.After(assets[j].ModTime)
		}
		return assets[i].Relative > assets[j].Relative
	})
	return assets, nil
}

// LatestVideoAsset returns the newest session video clip, if any.
func LatestVideoAsset(sessionPath string) (VideoAsset, bool, error) {
	assets, err := ListVideoAssets(sessionPath)
	if err != nil || len(assets) == 0 {
		return VideoAsset{}, false, err
	}
	return assets[0], true, nil
}

// ResolveVideoPath resolves an absolute path or session-relative videos/ ref.
func ResolveVideoPath(sessionPath, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("video path required")
	}
	if filepath.IsAbs(ref) {
		return validateVideoFile(ref)
	}
	clean := filepath.Clean(filepath.FromSlash(ref))
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid session video path")
	}
	if dir := filepath.Dir(clean); dir != "videos" && dir != "." {
		return "", errors.New("session videos must stay under videos/")
	}
	if dir := filepath.Dir(clean); dir == "." {
		clean = filepath.Join("videos", clean)
	}
	root, err := videoAssetRoot(sessionPath)
	if err != nil {
		return "", err
	}
	return validateVideoFile(filepath.Join(filepath.Dir(root), clean))
}

func videoAssetRoot(sessionPath string) (string, error) {
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifact, "videos"), nil
}

func validateVideoFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("open video: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("video must be a regular file")
	}
	if !videoAssetExtensions[strings.ToLower(filepath.Ext(path))] {
		return "", fmt.Errorf("unsupported video type %q", filepath.Ext(path))
	}
	return path, nil
}
