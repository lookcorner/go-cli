package tui

import (
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
)

func (m *model) listSessionDownloads() string {
	sessionPath := ""
	if m != nil && m.runner != nil {
		sessionPath = strings.TrimSpace(m.runner.SessionPath)
	}
	if sessionPath == "" {
		return "No session path available for download discovery."
	}
	assets, err := session.ListDownloadAssets(sessionPath)
	if err != nil {
		return "Couldn't list session downloads: " + err.Error()
	}
	if len(assets) == 0 {
		return "No session downloads yet. web_fetch PDFs land in artifacts/<session>/downloads/."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Session downloads (%d):\n", len(assets)))
	for index, asset := range assets {
		b.WriteString(fmt.Sprintf("%d. `%s` (%d bytes)\n", index+1, asset.URI, asset.Size))
	}
	b.WriteString("\nOpen with `read_file` on the path shown by web_fetch, or copy `downloads/N.pdf`.")
	return strings.TrimSpace(b.String())
}
