package marketplace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestComponentCatalogEnrichesIndexedEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "plugins", "demo", "plugin.json"), `{"name":"manifest-name"}`)
	mustWrite(t, filepath.Join(root, ".grok-plugin", "marketplace.json"), `{"plugins":[
  {"name":"index-name","keywords":["code"],"domains":["example.com"],"homepage":"https://example.com","source":"plugins/demo"},
  {"name":"remote","source":{"source":"url","url":"https://example.com/plugin.git","sha":"abc"}},
  {"name":"mismatch","source":{"source":"url","url":"https://example.com/other.git","sha":"wrong"}}
]}`)
	mustWrite(t, filepath.Join(root, ".grok-plugin", "plugin-index.json"), `{"version":1,"plugins":{
  "index-name":{"components":{"skills":[{"name":"review","description":"Review code"}]}},
  "remote":{"sha":"abc","components":{"commands":[{"name":"/run"}]}},
  "mismatch":{"sha":"expected","components":{"agents":[{"name":"worker"}]}}
}}`)

	entries := scanRoot(root, root)
	if len(entries) != 3 || entries[0].Name != "manifest-name" || entries[0].Components == nil || entries[0].Components.Skills[0].Name != "review" || entries[0].Keywords[0] != "code" || entries[0].Domains[0] != "example.com" || entries[0].Homepage != "https://example.com" {
		t.Fatalf("local entry=%#v", entries)
	}
	if entries[1].Components == nil || entries[1].Components.Commands[0].Name != "/run" || entries[2].Components != nil {
		t.Fatalf("remote entries=%#v", entries)
	}
}

func TestComponentCatalogBrokenPreferredFileDoesNotFallback(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".grok-plugin", "plugin-index.json"), "not json")
	mustWrite(t, filepath.Join(root, ".claude-plugin", "plugin-index.json"), `{"version":1,"plugins":{"demo":{"components":{"skills":[{"name":"stale"}]}}}}`)
	if catalog := loadComponentCatalog(root); catalog != nil {
		t.Fatalf("catalog=%#v", catalog)
	}
}

func TestComponentCatalogSanitizesAndCapsUntrustedItems(t *testing.T) {
	root := t.TempDir()
	items := strings.Repeat(`{"name":"a\u001b[31mb","description":"x\u0007y"},`, 50) + `{"name":"discarded"}`
	mustWrite(t, filepath.Join(root, ".grok-plugin", "plugin-index.json"), `{"version":1,"plugins":{"demo":{"components":{"skills":[`+items+`]}}}}`)
	catalog := loadComponentCatalog(root)
	components := catalog.components("demo", "")
	if len(components.Skills) != 50 || components.Skills[0].Name != "a[31mb" || components.Skills[0].Description != "xy" {
		t.Fatalf("components=%#v", components)
	}
}
