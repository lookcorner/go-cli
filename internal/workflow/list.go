// Package workflow discovers named Rhai workflow scripts for ACP listing.
// Full Rhai execution remains out of scope; this package only scans and parses meta.
package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const maxWorkflowSourceBytes = 1024 * 1024
const maxWorkflowNameBytes = 64

// Listing matches Rust WorkflowListing JSON for x.ai/workflows/list.
type Listing struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	WhenToUse   *string `json:"when_to_use,omitempty"`
	Source      string  `json:"source"`
	Path        *string `json:"path,omitempty"`
}

var metaFieldRe = regexp.MustCompile(`(?m)^\s*(name|description|when_to_use)\s*:\s*"((?:\\.|[^"\\])*)"`)

// List scans builtin, project (.grok/workflows), and user ($GROK_HOME/workflows)
// scopes with first-wins merge, matching Rust WorkflowRegistry::scan.
func List(cwd string) []Listing {
	entries := make([]Listing, 0, 8)
	seen := map[string]struct{}{}
	add := func(items []Listing) {
		for _, item := range items {
			if _, ok := seen[item.Name]; ok {
				continue
			}
			seen[item.Name] = struct{}{}
			entries = append(entries, item)
		}
	}
	add(builtinListings())
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		root := workspace.GitRoot(cwd)
		add(scanDirectory(filepath.Join(root, ".grok", "workflows"), "project"))
	}
	if home, err := config.PolicyHome(); err == nil && home != "" {
		add(scanDirectory(filepath.Join(home, "workflows"), "user"))
	}
	return entries
}

func builtinListings() []Listing {
	return []Listing{{
		Name:        "deep-research",
		Description: "Research a query with bounded parallelism, cross-check the evidence, and write a cited report",
		Source:      "builtin",
	}}
}

func scanDirectory(dir, source string) []Listing {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".rhai") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)

	type candidate struct {
		listing Listing
		name    string
	}
	var candidates []candidate
	counts := map[string]int{}
	for _, path := range paths {
		listing, ok := loadListing(path, source)
		if !ok {
			continue
		}
		counts[listing.Name]++
		candidates = append(candidates, candidate{listing: listing, name: listing.Name})
	}
	out := make([]Listing, 0, len(candidates))
	for _, item := range candidates {
		if counts[item.name] > 1 {
			continue
		}
		out = append(out, item.listing)
	}
	return out
}

func loadListing(path, source string) (Listing, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Listing{}, false
	}
	if info.Size() > maxWorkflowSourceBytes {
		return Listing{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) > maxWorkflowSourceBytes {
		return Listing{}, false
	}
	meta, ok := parseMeta(string(data))
	if !ok {
		return Listing{}, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".rhai")
	if !validWorkflowName(stem) || stem != meta.name {
		return Listing{}, false
	}
	pathCopy := path
	listing := Listing{
		Name:        meta.name,
		Description: meta.description,
		Source:      source,
		Path:        &pathCopy,
	}
	if meta.whenToUse != "" {
		value := meta.whenToUse
		listing.WhenToUse = &value
	}
	return listing, true
}

type workflowMeta struct {
	name        string
	description string
	whenToUse   string
}

func parseMeta(script string) (workflowMeta, bool) {
	block, ok := extractMetaMap(script)
	if !ok {
		return workflowMeta{}, false
	}
	fields := map[string]string{}
	for _, match := range metaFieldRe.FindAllStringSubmatch(block, -1) {
		if len(match) != 3 {
			continue
		}
		fields[match[1]] = unescapeRhaiString(match[2])
	}
	name := strings.TrimSpace(fields["name"])
	description := strings.TrimSpace(fields["description"])
	if !validWorkflowName(name) || description == "" {
		return workflowMeta{}, false
	}
	return workflowMeta{
		name:        name,
		description: description,
		whenToUse:   strings.TrimSpace(fields["when_to_use"]),
	}, true
}

func extractMetaMap(script string) (string, bool) {
	const prefix = "let meta = #{"
	start := strings.Index(script, prefix)
	if start < 0 {
		return "", false
	}
	i := start + len(prefix)
	depth := 1
	inString := false
	escaped := false
	for i < len(script) && depth > 0 {
		ch := script[i]
		if inString {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			i++
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	if depth != 0 {
		return "", false
	}
	return script[start+len(prefix) : i-1], true
}

func unescapeRhaiString(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	escaped := false
	for _, r := range value {
		if escaped {
			switch r {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validWorkflowName(name string) bool {
	if name == "" || len(name) > maxWorkflowNameBytes {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' {
			continue
		}
		return false
	}
	return true
}
