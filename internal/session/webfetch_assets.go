package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WebFetchAsset is one confined overflow file under artifacts/<session>/web_fetch/.
type WebFetchAsset struct {
	Name    string // basename, e.g. "1.md"
	URI     string // confined web_fetch/<name>
	Path    string // absolute filesystem path
	Size    int64
	ModTime int64 // unix seconds
}

// ListWebFetchAssets returns truncated web_fetch text/markdown overflow files,
// newest first. Missing web_fetch/ is an empty list, not an error. Symlinked
// web_fetch/ directories and symlink entries are rejected.
func ListWebFetchAssets(sessionPath string) ([]WebFetchAsset, error) {
	root, err := webFetchAssetRoot(sessionPath)
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
		return nil, errors.New("session web_fetch must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []WebFetchAsset
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
		out = append(out, WebFetchAsset{
			Name:    name,
			URI:     filepath.ToSlash(filepath.Join("web_fetch", name)),
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

// ResolveWebFetchAsset maps a user argument to a session web_fetch path.
// Accepts absolute paths, web_fetch/<name>, or bare basenames under web_fetch/.
func ResolveWebFetchAsset(sessionPath, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty web_fetch path")
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	clean := filepath.Clean(filepath.FromSlash(arg))
	base := filepath.Base(clean)
	if clean == base || filepath.Dir(clean) == "web_fetch" {
		assets, err := ListWebFetchAssets(sessionPath)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			if asset.Name == base {
				return asset.Path, nil
			}
		}
		return "", fmt.Errorf("session web_fetch %q not found", base)
	}
	return "", fmt.Errorf("invalid session web_fetch path %q", arg)
}

// LatestWebFetchAsset returns the newest web_fetch overflow file, if any.
func LatestWebFetchAsset(sessionPath string) (WebFetchAsset, bool, error) {
	assets, err := ListWebFetchAssets(sessionPath)
	if err != nil {
		return WebFetchAsset{}, false, err
	}
	if len(assets) == 0 {
		return WebFetchAsset{}, false, nil
	}
	return assets[0], true, nil
}

func webFetchAssetRoot(sessionPath string) (string, error) {
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifact, "web_fetch"), nil
}
