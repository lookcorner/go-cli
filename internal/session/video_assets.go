package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// VideoAsset is one confined clip under the session artifact videos/ directory.
type VideoAsset struct {
	Name    string // basename, e.g. "1.mp4"
	URI     string // confined videos/<name>
	Path    string // absolute filesystem path
	Size    int64
	ModTime int64 // unix seconds
}

var videoAssetExts = map[string]bool{
	".mp4":  true,
	".webm": true,
	".mov":  true,
	".mkv":  true,
}

// ListVideoAssets returns artifacts/<session>/videos clips (and a few common
// containers), newest first. Missing videos/ is an empty list, not an error.
// Symlinked videos/ directories are rejected.
func ListVideoAssets(sessionPath string) ([]VideoAsset, error) {
	root, err := videoAssetRoot(sessionPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("session videos must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []VideoAsset
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !videoAssetExts[ext] {
			continue
		}
		path := filepath.Join(root, name)
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, VideoAsset{
			Name:    name,
			URI:     filepath.ToSlash(filepath.Join("videos", name)),
			Path:    path,
			Size:    st.Size(),
			ModTime: st.ModTime().Unix(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ModTime != out[j].ModTime {
			return out[i].ModTime > out[j].ModTime
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// ResolveVideoAsset maps a user argument to a session video path.
// Accepts absolute paths, videos/<name>, or bare basenames under videos/.
func ResolveVideoAsset(sessionPath, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty video path")
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	clean := filepath.Clean(filepath.FromSlash(arg))
	base := filepath.Base(clean)
	if clean == base || filepath.Dir(clean) == "videos" {
		assets, err := ListVideoAssets(sessionPath)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			if asset.Name == base {
				return asset.Path, nil
			}
		}
		return "", fmt.Errorf("session video %q not found", base)
	}
	return "", fmt.Errorf("invalid session video path %q", arg)
}

// LatestVideoAsset returns the newest session clip, or false when none exist.
func LatestVideoAsset(sessionPath string) (VideoAsset, bool, error) {
	assets, err := ListVideoAssets(sessionPath)
	if err != nil {
		return VideoAsset{}, false, err
	}
	if len(assets) == 0 {
		return VideoAsset{}, false, nil
	}
	return assets[0], true, nil
}

func videoAssetRoot(sessionPath string) (string, error) {
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifact, "videos"), nil
}
