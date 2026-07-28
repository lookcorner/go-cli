package tui

import (
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
)

func (m *model) listSessionWebFetch() string {
	sessionPath := ""
	if m != nil && m.runner != nil {
		sessionPath = strings.TrimSpace(m.runner.SessionPath)
	}
	if sessionPath == "" {
		return "No session path available for web_fetch discovery."
	}
	assets, err := session.ListWebFetchAssets(sessionPath)
	if err != nil {
		return "Couldn't list web_fetch artifacts: " + err.Error()
	}
	if len(assets) == 0 {
		return "No truncated web_fetch artifacts yet. Oversized fetches land in artifacts/<session>/web_fetch/."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("web_fetch artifacts (%d):\n", len(assets)))
	for index, asset := range assets {
		b.WriteString(fmt.Sprintf("%d. `%s` (%d bytes)\n", index+1, asset.URI, asset.Size))
	}
	b.WriteString("\nOpen with `read_file` using the path shown by web_fetch, or copy `web_fetch/N.md`.")
	return strings.TrimSpace(b.String())
}
