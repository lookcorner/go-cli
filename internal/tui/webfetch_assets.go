package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/session"
)

const maxWebFetchShowBytes = 32 << 10

func (m *model) listSessionWebFetch() string {
	return m.sessionWebFetch("")
}

func (m *model) sessionWebFetch(arg string) string {
	sessionPath := ""
	if m != nil && m.runner != nil {
		sessionPath = strings.TrimSpace(m.runner.SessionPath)
	}
	if sessionPath == "" {
		return "No session path available for web_fetch discovery."
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
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
		b.WriteString("\nShow one with `/fetched web_fetch/N.md` (or bare name).")
		return strings.TrimSpace(b.String())
	}
	path, err := session.ResolveWebFetchAsset(sessionPath, arg)
	if err != nil {
		return "Couldn't open web_fetch artifact: " + err.Error()
	}
	info, err := os.Stat(path)
	if err != nil {
		return "Couldn't read web_fetch artifact: " + err.Error()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "Couldn't read web_fetch artifact: " + err.Error()
	}
	uri := filepath.ToSlash(filepath.Join("web_fetch", filepath.Base(path)))
	truncated := false
	if len(data) > maxWebFetchShowBytes {
		data = data[:maxWebFetchShowBytes]
		truncated = true
		for !utf8.Valid(data) && len(data) > 0 {
			data = data[:len(data)-1]
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("`%s` (%d bytes", uri, info.Size()))
	if truncated {
		b.WriteString(fmt.Sprintf("; showing first %d", len(data)))
	}
	b.WriteString("):\n\n")
	b.Write(data)
	if truncated {
		b.WriteString("\n\n[truncated]")
	}
	return strings.TrimSpace(b.String())
}
