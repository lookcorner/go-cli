package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/plugin"
	"gopkg.in/yaml.v3"
)

// Definition is the portable part of an agents/*.md file. Execution belongs
// to the future subagent application flow, not to discovery.
type Definition struct {
	Name            string
	Description     string
	Tools           []string
	DisallowedTools []string
	MaxTurns        int
	Prompt          string
	Path            string
	Plugin          string
}

type frontmatter struct {
	Name            string     `yaml:"name"`
	Description     string     `yaml:"description"`
	Tools           stringList `yaml:"tools"`
	DisallowedTools stringList `yaml:"disallowedTools"`
	MaxTurns        int        `yaml:"maxTurns"`
}

type stringList []string

func (s *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		for _, value := range strings.Split(node.Value, ",") {
			if value = strings.TrimSpace(value); value != "" {
				*s = append(*s, value)
			}
		}
		return nil
	case yaml.SequenceNode:
		return node.Decode((*[]string)(s))
	default:
		return fmt.Errorf("agent tool list must be a string or list")
	}
}

func DiscoverPlugins(plugins []plugin.Plugin) ([]Definition, []error) {
	var definitions []Definition
	var errors []error
	seen := make(map[string]bool)
	for _, item := range plugins {
		if !item.Executable {
			continue
		}
		loaded, loadErrors := InspectPlugin(item)
		errors = append(errors, loadErrors...)
		for _, definition := range loaded {
			if !seen[definition.Name] {
				seen[definition.Name] = true
				definitions = append(definitions, definition)
			}
		}
	}
	return definitions, errors
}

func InspectPlugin(item plugin.Plugin) ([]Definition, []error) {
	var definitions []Definition
	var errors []error
	for _, dir := range item.AgentDirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
		if err != nil {
			errors = append(errors, err)
			continue
		}
		sort.Strings(paths)
		for _, path := range paths {
			definition, err := Parse(path, item.Name)
			if err != nil {
				errors = append(errors, err)
				continue
			}
			definitions = append(definitions, definition)
		}
	}
	return definitions, errors
}

func PluginNames(item plugin.Plugin) []string {
	seen := make(map[string]bool)
	for _, dir := range item.AgentDirs {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		for _, path := range paths {
			seen[strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func Parse(path, pluginName string) (Definition, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Definition{}, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Definition{}, fmt.Errorf("parse agent %q: YAML frontmatter is required", path)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return Definition{}, fmt.Errorf("parse agent %q: unterminated YAML frontmatter", path)
	}
	var metadata frontmatter
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &metadata); err != nil {
		return Definition{}, fmt.Errorf("parse agent %q: %w", path, err)
	}
	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if name == "" || strings.TrimSpace(metadata.Description) == "" {
		return Definition{}, fmt.Errorf("parse agent %q: name and description are required", path)
	}
	if metadata.MaxTurns < 0 {
		return Definition{}, fmt.Errorf("parse agent %q: maxTurns must not be negative", path)
	}
	return Definition{
		Name: name, Description: strings.TrimSpace(metadata.Description), Tools: metadata.Tools,
		DisallowedTools: metadata.DisallowedTools, MaxTurns: metadata.MaxTurns,
		Prompt: strings.TrimSpace(text[4+end+5:]), Path: path, Plugin: pluginName,
	}, nil
}
