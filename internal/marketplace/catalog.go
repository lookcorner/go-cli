package marketplace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"unicode"
)

type Component struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Components struct {
	Skills     []Component `json:"skills,omitempty"`
	Commands   []Component `json:"commands,omitempty"`
	Agents     []Component `json:"agents,omitempty"`
	MCPServers []Component `json:"mcpServers,omitempty"`
	Hooks      []Component `json:"hooks,omitempty"`
	LSPServers []Component `json:"lspServers,omitempty"`
}

type componentCatalog struct {
	Version int                              `json:"version"`
	Plugins map[string]componentCatalogEntry `json:"plugins"`
}

type componentCatalogEntry struct {
	SHA        string     `json:"sha"`
	Components Components `json:"components"`
}

func loadComponentCatalog(root string) *componentCatalog {
	for _, dir := range []string{".grok-plugin", ".claude-plugin"} {
		data, err := os.ReadFile(filepath.Join(root, dir, "plugin-index.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil
		}
		var catalog componentCatalog
		if json.Unmarshal(data, &catalog) != nil || catalog.Version != 1 {
			return nil
		}
		for name, entry := range catalog.Plugins {
			entry.Components.sanitize()
			catalog.Plugins[name] = entry
		}
		return &catalog
	}
	return nil
}

func (catalog *componentCatalog) components(name, sha string) *Components {
	entry, ok := catalog.Plugins[name]
	if !ok || sha != "" && entry.SHA != sha {
		return nil
	}
	return &entry.Components
}

func (components *Components) sanitize() {
	for _, items := range []*[]Component{&components.Skills, &components.Commands, &components.Agents, &components.MCPServers, &components.Hooks, &components.LSPServers} {
		if len(*items) > 50 {
			*items = (*items)[:50]
		}
		for index := range *items {
			(*items)[index].Name = cleanComponentText((*items)[index].Name)
			(*items)[index].Description = cleanComponentText((*items)[index].Description)
		}
	}
}

func cleanComponentText(value string) string {
	runes := make([]rune, 0, min(120, len(value)))
	for _, current := range value {
		if unicode.IsControl(current) || current >= '\u200b' && current <= '\u200f' || current >= '\u202a' && current <= '\u202e' || current >= '\u2066' && current <= '\u2069' || current == '\ufeff' {
			continue
		}
		runes = append(runes, current)
		if len(runes) == 120 {
			break
		}
	}
	return string(runes)
}
