package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
	"gopkg.in/yaml.v3"
)

const (
	maxSkillBytes = 1 << 20
	maxSkills     = 500
)

type Skill struct {
	Name        string
	Description string
	Path        string
	Source      string
	Paths       []string
}

type Catalog struct {
	root    string
	byName  map[string]Skill
	pending map[string]Skill
}

func Discover(workspaceRoot string) (*Catalog, error) {
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	return discover(workspaceRoot, home, grokHome)
}

func discover(workspaceRoot, home, grokHome string) (*Catalog, error) {
	if real, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceRoot = real
	}
	type root struct {
		path   string
		source string
	}
	var roots []root
	if home != "" {
		roots = append(roots,
			root{filepath.Join(home, ".cursor", "skills"), "user:cursor"},
			root{filepath.Join(home, ".claude", "skills"), "user:claude"},
			root{filepath.Join(home, ".agents", "skills"), "user:agents"},
		)
	}
	if grokHome != "" {
		roots = append(roots, root{filepath.Join(grokHome, "skills"), "user:grok"})
	}
	gitRoot := workspace.GitRoot(workspaceRoot)
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		for _, dir := range []string{".cursor", ".claude", ".agents", ".gork", ".grok"} {
			roots = append(roots, root{
				filepath.Join(scope, dir, "skills"), "workspace:" + strings.TrimPrefix(dir, "."),
			})
		}
	}
	catalog := &Catalog{root: workspaceRoot, byName: make(map[string]Skill), pending: make(map[string]Skill)}
	for _, root := range roots {
		if root.path == "" {
			continue
		}
		if err := catalog.scan(root.path, root.source); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func (c *Catalog) scan(root, source string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat skills root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxSkillBytes {
			return fmt.Errorf("skill %q exceeds %d bytes", path, maxSkillBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("skill %q is not UTF-8", path)
		}
		name, description, paths := parseMetadata(string(data), filepath.Base(filepath.Dir(path)))
		if name == "" {
			return nil
		}
		if _, active := c.byName[name]; !active {
			if _, held := c.pending[name]; !held && len(c.byName)+len(c.pending) >= maxSkills {
				return errors.New("skill discovery exceeded 500 skills")
			}
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve skill %q: %w", path, err)
		}
		// Later roots have higher priority, so workspace skills override user skills.
		delete(c.byName, name)
		delete(c.pending, name)
		skill := Skill{Name: name, Description: description, Path: real, Source: source, Paths: paths}
		if len(paths) == 0 {
			c.byName[name] = skill
		} else {
			if c.pending == nil {
				c.pending = make(map[string]Skill)
			}
			c.pending[name] = skill
		}
		return nil
	})
}

func (c *Catalog) Count() int {
	if c == nil {
		return 0
	}
	return len(c.byName) + len(c.pending)
}

// Activate makes paths-gated skills visible after a successful file tool call.
// It returns a synthetic reminder for the next model step.
func (c *Catalog) Activate(toolName string, raw json.RawMessage) string {
	if c == nil || len(c.pending) == 0 {
		return ""
	}
	path := toolPath(toolName, raw)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.root, path)
	}
	rel, err := filepath.Rel(c.root, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	var activated []string
	for name, skill := range c.pending {
		if matchesPaths(skill.Paths, rel) {
			c.byName[name] = skill
			delete(c.pending, name)
			activated = append(activated, name)
		}
	}
	if len(activated) == 0 {
		return ""
	}
	sort.Strings(activated)
	var output strings.Builder
	output.WriteString("<system-reminder>\nNew skills became available after accessing ")
	output.WriteString(rel)
	output.WriteString(":\n")
	for _, name := range activated {
		skill := c.byName[name]
		fmt.Fprintf(&output, "- %s: %s (%s)\n", name, skill.Description, skill.Source)
	}
	output.WriteString("Use the skill tool to load one when it matches the task.\n</system-reminder>")
	return output.String()
}

func toolPath(name string, raw json.RawMessage) string {
	switch name {
	case "read_file", "write_file", "edit_file", "search_replace", "list_dir", "list_files":
	default:
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil || json.Unmarshal([]byte(encoded), &values) != nil {
			return ""
		}
	}
	for _, key := range []string{"target_file", "file_path", "target_directory", "path"} {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	if name == "list_dir" || name == "list_files" {
		return "."
	}
	return ""
}

func matchesPaths(patterns []string, rel string) bool {
	matched := false
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "/")
		if strings.HasSuffix(pattern, "/") {
			pattern += "**"
		}
		if !strings.Contains(pattern, "/") {
			pattern = "**/" + pattern
		}
		if patternMatches(pattern, rel) {
			matched = !negated
		}
	}
	return matched
}

func patternMatches(pattern, rel string) bool {
	for current := rel; current != "."; current = filepath.ToSlash(filepath.Dir(current)) {
		matched, err := doublestar.Match(pattern, current)
		if err != nil {
			return false
		}
		if matched {
			return true
		}
	}
	return false
}

func (c *Catalog) Summary() string {
	if c == nil || len(c.byName) == 0 {
		return ""
	}
	names := c.Names()
	var output strings.Builder
	output.WriteString("Available skills are listed below. Use the skill tool to load a skill's complete instructions when the user names it or the task clearly matches it.\n")
	for _, name := range names {
		skill := c.byName[name]
		fmt.Fprintf(&output, "- %s: %s (%s)\n", skill.Name, skill.Description, skill.Source)
	}
	return output.String()
}

func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.byName))
	for name := range c.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) Tool() *Tool { return &Tool{catalog: c} }

type Tool struct{ catalog *Catalog }

func (t *Tool) Definition() api.ToolDefinition {
	names := t.catalog.Names()
	nameSchema := map[string]any{"type": "string"}
	if len(names) > 0 {
		nameSchema["enum"] = names
	}
	return api.ToolDefinition{
		Type: "function", Name: "skill",
		Description: "Load the complete SKILL.md instructions for one discovered skill. Available names: " + strings.Join(names, ", "),
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": nameSchema},
			"required":   []string{"name"}, "additionalProperties": false,
		},
	}
}

func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode skill arguments: %w", err)
	}
	skill, ok := t.catalog.byName[args.Name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", args.Name)
	}
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", args.Name, err)
	}
	if len(data) > maxSkillBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("skill %q is too large or no longer UTF-8", args.Name)
	}
	return fmt.Sprintf("Skill: %s\nSource: %s\nPath: %s\n\n%s", skill.Name, skill.Source, skill.Path, data), nil
}

func parseMetadata(content, fallbackName string) (string, string, []string) {
	name := fallbackName
	if !strings.HasPrefix(content, "---\n") {
		return name, "", nil
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return name, "", nil
	}
	var metadata struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Paths       []string `yaml:"paths"`
	}
	if yaml.Unmarshal([]byte(content[4:4+end]), &metadata) != nil {
		return name, "", nil
	}
	if metadata.Name != "" {
		name = metadata.Name
	}
	return name, metadata.Description, metadata.Paths
}
