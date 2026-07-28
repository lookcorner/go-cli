package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ImageAsset is one confined file under the session artifact images/ directory.
type ImageAsset struct {
	Name    string // basename, e.g. "1.png"
	URI     string // confined images/<name>
	Path    string // absolute filesystem path
	Size    int64
	ModTime int64 // unix seconds
}

var imageAssetExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".tif":  true,
	".tiff": true,
}

// ListImageAssets returns artifacts/<session>/images files, newest first.
// Missing images/ is an empty list, not an error. Symlinked images/ directories
// and symlink entries are rejected.
func ListImageAssets(sessionPath string) ([]ImageAsset, error) {
	root, err := imageAssetRoot(sessionPath)
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
		return nil, errors.New("session images must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []ImageAsset
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !imageAssetExts[ext] {
			continue
		}
		path := filepath.Join(root, name)
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, ImageAsset{
			Name:    name,
			URI:     filepath.ToSlash(filepath.Join("images", name)),
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

// ResolveImageAsset maps a user argument to a session image path.
// Accepts absolute paths, images/<name>, or bare basenames under images/.
func ResolveImageAsset(sessionPath, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty image path")
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	clean := filepath.Clean(filepath.FromSlash(arg))
	base := filepath.Base(clean)
	if clean == base || filepath.Dir(clean) == "images" {
		assets, err := ListImageAssets(sessionPath)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			if asset.Name == base {
				return asset.Path, nil
			}
		}
		return "", fmt.Errorf("session image %q not found", base)
	}
	return "", fmt.Errorf("invalid session image path %q", arg)
}

// LatestImageAsset returns the newest session image, or false when none exist.
func LatestImageAsset(sessionPath string) (ImageAsset, bool, error) {
	assets, err := ListImageAssets(sessionPath)
	if err != nil {
		return ImageAsset{}, false, err
	}
	if len(assets) == 0 {
		return ImageAsset{}, false, nil
	}
	return assets[0], true, nil
}

func imageAssetRoot(sessionPath string) (string, error) {
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifact, "images"), nil
}
