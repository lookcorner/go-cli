package docs

import (
	"strings"
	"testing"
)

func TestCatalogIsCompleteAndDefensivelyCopied(t *testing.T) {
	items := All()
	want := []string{
		"Getting Started", "Authentication", "Keyboard Shortcuts", "Slash Commands", "Configuration", "Theming and Appearance",
		"MCP Servers", "Skills", "Plugins and Marketplace", "Hooks", "Custom Models", "Project Rules (AGENTS.md)", "Memory",
		"Headless Mode and Scripting", "Agent Mode and IDE Integration", "Subagents and Personas", "Session Management", "Sandbox Mode",
		"Plan Mode", "Background Tasks and Monitoring", "Terminal Support and Troubleshooting", "Permissions and Safety",
		"Hooks & Plugins Guide", "Creating Custom Hooks",
	}
	if len(items) != len(want) {
		t.Fatalf("guides=%d want=%d", len(items), len(want))
	}
	seen := make(map[string]bool, len(items))
	for index, item := range items {
		if item.Title != want[index] {
			t.Fatalf("guide[%d]=%q want=%q", index, item.Title, want[index])
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Description) == "" || !strings.HasPrefix(item.Content, "# "+item.Title) {
			t.Fatalf("invalid guide=%#v", item)
		}
		key := strings.ToLower(item.Title)
		if seen[key] {
			t.Fatalf("duplicate title=%q", item.Title)
		}
		seen[key] = true
	}
	items[0].Title = "changed"
	if All()[0].Title != "Getting Started" {
		t.Fatal("All exposed mutable catalog storage")
	}
}

func TestFindIsTrimmedCaseInsensitiveAndExact(t *testing.T) {
	item, ok := Find("  getting STARTED ")
	if !ok || item.Title != "Getting Started" || item.Content == "" {
		t.Fatalf("item=%#v ok=%v", item, ok)
	}
	for _, title := range []string{"", "Getting", "not-a-real-guide"} {
		if item, ok := Find(title); ok {
			t.Fatalf("Find(%q)=%#v,true", title, item)
		}
	}
}
