package tui

import (
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
)

func (m *model) listSessionImages() string {
	sessionPath := ""
	if m != nil && m.runner != nil {
		sessionPath = strings.TrimSpace(m.runner.SessionPath)
	}
	if sessionPath == "" {
		return "No session path available for image discovery."
	}
	assets, err := session.ListImageAssets(sessionPath)
	if err != nil {
		return "Couldn't list session images: " + err.Error()
	}
	if len(assets) == 0 {
		return "No session images yet. web_fetch images land in artifacts/<session>/images/."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Session images (%d):\n", len(assets)))
	for index, asset := range assets {
		b.WriteString(fmt.Sprintf("%d. `%s` (%d bytes)\n", index+1, asset.URI, asset.Size))
	}
	b.WriteString("\nPreview with `/preview-image` (latest transcript or session image).")
	return strings.TrimSpace(b.String())
}
