package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
)

func (m *model) sessionPath() string {
	if m == nil || m.runner == nil {
		return ""
	}
	return strings.TrimSpace(m.runner.SessionPath)
}

func (m *model) resolvePlayVideoPath(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	sessionPath := m.sessionPath()
	if ref == "" {
		if sessionPath == "" {
			return "", fmt.Errorf("no session videos found")
		}
		latest, ok, err := session.LatestVideoAsset(sessionPath)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no session videos found")
		}
		return latest.Path, nil
	}
	if sessionPath != "" {
		if resolved, err := session.ResolveVideoPath(sessionPath, ref); err == nil {
			return resolved, nil
		} else if filepath.IsAbs(ref) {
			return "", err
		} else if !filepath.IsAbs(ref) && (strings.HasPrefix(filepath.ToSlash(ref), "videos/") || !strings.Contains(filepath.ToSlash(ref), "/")) {
			return "", err
		}
	}
	if filepath.IsAbs(ref) {
		return ref, nil
	}
	if m.workspace != "" {
		return filepath.Join(m.workspace, filepath.FromSlash(ref)), nil
	}
	return filepath.Clean(filepath.FromSlash(ref)), nil
}

func (m *model) listSessionVideos() string {
	sessionPath := m.sessionPath()
	if sessionPath == "" {
		return "No session path available for video discovery."
	}
	assets, err := session.ListVideoAssets(sessionPath)
	if err != nil {
		return "Couldn't list session videos: " + err.Error()
	}
	if len(assets) == 0 {
		return "No session videos yet. Generated clips land in artifacts/<session>/videos/."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Session videos (%d):\n", len(assets)))
	for index, asset := range assets {
		b.WriteString(fmt.Sprintf("%d. `%s` (%d bytes)\n", index+1, asset.Relative, asset.Size))
	}
	b.WriteString("\nPlay with `/play-video` (latest) or `/play-video videos/N.mp4`.")
	return strings.TrimSpace(b.String())
}
