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

	"github.com/lookcorner/go-cli/internal/api"
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
}

type Catalog struct {
	byName map[string]Skill
}

func Discover(workspaceRoot string) (*Catalog, error) {
	home, _ := os.UserHomeDir()
	roots := []struct {
		path   string
		source string
	}{
		{filepath.Join(home, ".grok", "skills"), "user:grok"},
		{filepath.Join(home, ".agents", "skills"), "user:agents"},
		{filepath.Join(home, ".claude", "skills"), "user:claude"},
		{filepath.Join(workspaceRoot, ".gork", "skills"), "workspace:grok"},
		{filepath.Join(workspaceRoot, ".agents", "skills"), "workspace:agents"},
		{filepath.Join(workspaceRoot, ".claude", "skills"), "workspace:claude"},
	}
	catalog := &Catalog{byName: make(map[string]Skill)}
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
		if len(c.byName) >= maxSkills {
			return errors.New("skill discovery exceeded 500 skills")
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
		name, description := parseMetadata(string(data), filepath.Base(filepath.Dir(path)))
		if name == "" {
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve skill %q: %w", path, err)
		}
		// Later roots have higher priority, so workspace skills override user skills.
		c.byName[name] = Skill{Name: name, Description: description, Path: real, Source: source}
		return nil
	})
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
	return api.ToolDefinition{
		Type: "function", Name: "skill",
		Description: "Load the complete SKILL.md instructions for one discovered skill. Available names: " + strings.Join(names, ", "),
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string", "enum": names}},
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

func parseMetadata(content, fallbackName string) (string, string) {
	name := fallbackName
	description := ""
	if !strings.HasPrefix(content, "---\n") {
		return name, description
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return name, description
	}
	for _, line := range strings.Split(content[4:4+end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			if value != "" {
				name = value
			}
		case "description":
			description = value
		}
	}
	return name, description
}
