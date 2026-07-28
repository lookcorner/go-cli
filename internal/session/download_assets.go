package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DownloadAsset is one confined file under the session artifact downloads/ tree.
type DownloadAsset struct {
	Name    string // basename, e.g. "1.pdf"
	URI     string // confined downloads/<name>
	Path    string // absolute filesystem path
	Size    int64
	ModTime int64 // unix seconds
}

// ListDownloadAssets returns artifacts/<session>/downloads files, newest first.
// Missing downloads/ is an empty list, not an error. Symlinked downloads/
// directories and symlink entries are rejected.
func ListDownloadAssets(sessionPath string) ([]DownloadAsset, error) {
	root, err := downloadAssetRoot(sessionPath)
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
		return nil, errors.New("session downloads must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []DownloadAsset
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(root, name)
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, DownloadAsset{
			Name:    name,
			URI:     filepath.ToSlash(filepath.Join("downloads", name)),
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

// ResolveDownloadAsset maps a user argument to a session download path.
// Accepts absolute paths, downloads/<name>, or bare basenames under downloads/.
func ResolveDownloadAsset(sessionPath, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty download path")
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	clean := filepath.Clean(filepath.FromSlash(arg))
	base := filepath.Base(clean)
	if clean == base || filepath.Dir(clean) == "downloads" {
		assets, err := ListDownloadAssets(sessionPath)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			if asset.Name == base {
				return asset.Path, nil
			}
		}
		return "", fmt.Errorf("session download %q not found", base)
	}
	return "", fmt.Errorf("invalid session download path %q", arg)
}

// LatestDownloadAsset returns the newest session download, or false when none exist.
func LatestDownloadAsset(sessionPath string) (DownloadAsset, bool, error) {
	assets, err := ListDownloadAssets(sessionPath)
	if err != nil {
		return DownloadAsset{}, false, err
	}
	if len(assets) == 0 {
		return DownloadAsset{}, false, nil
	}
	return assets[0], true, nil
}

func downloadAssetRoot(sessionPath string) (string, error) {
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifact, "downloads"), nil
}
