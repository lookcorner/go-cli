package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
	"gopkg.in/yaml.v3"
)

type Definition struct {
	Name            string
	Description     string
	Tools           []string
	DisallowedTools []string
	MaxTurns        int
	Prompt          string
	Path            string
	Plugin          string
	Scope           string
	Model           string
	Effort          string
	PermissionMode  string
	Isolation       string
	Background      *bool
	InitialPrompt   string
	Builtin         bool
}

type frontmatter struct {
	Name            string     `yaml:"name"`
	Description     string     `yaml:"description"`
	Tools           stringList `yaml:"tools"`
	DisallowedTools stringList `yaml:"disallowedTools"`
	MaxTurns        int        `yaml:"maxTurns"`
	Model           string     `yaml:"model"`
	Effort          string     `yaml:"effort"`
	PermissionMode  string     `yaml:"permissionMode"`
	Isolation       string     `yaml:"isolation"`
	Background      *bool      `yaml:"background"`
	InitialPrompt   string     `yaml:"initialPrompt"`
}

type Config struct {
	WorkspaceRoot  string
	ProjectTrusted bool
	Compat         compat.Config
	Plugins        []plugin.Plugin
}

type Catalog struct {
	definitions []Definition
	byName      map[string]Definition
}

func Discover(config Config) (*Catalog, []error) {
	definitions := builtinDefinitions()
	byName := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	var errors []error
	add := func(dir, scope string, canShadowBuiltin bool) {
		loaded, loadErrors := inspectDir(dir, "", scope)
		errors = append(errors, loadErrors...)
		for _, definition := range loaded {
			existing, found := byName[definition.Name]
			if found && (!existing.Builtin || !canShadowBuiltin) {
				continue
			}
			if found {
				for index := range definitions {
					if definitions[index].Name == definition.Name {
						definitions[index] = definition
						break
					}
				}
			} else {
				definitions = append(definitions, definition)
			}
			byName[definition.Name] = definition
		}
	}
	if config.ProjectTrusted {
		cwd := config.WorkspaceRoot
		if real, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = real
		}
		if root, ok := workspace.FindGitRoot(cwd); ok {
			scopes := workspace.ProjectScopes(root, cwd)
			for index := len(scopes) - 1; index >= 0; index-- {
				add(filepath.Join(scopes[index], ".grok", "agents"), "project", true)
				if config.Compat.Claude.Agents {
					add(filepath.Join(scopes[index], ".claude", "agents"), "project", true)
				}
			}
		}
	}
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	if grokHome != "" {
		add(filepath.Join(grokHome, "agents"), "user", false)
	}
	if home != "" && grokHome != filepath.Join(home, ".grok") {
		add(filepath.Join(home, ".grok", "agents"), "user", false)
	}
	if config.Compat.Claude.Agents && home != "" {
		add(filepath.Join(home, ".claude", "agents"), "user", false)
	}
	if grokHome != "" {
		add(filepath.Join(grokHome, "bundled", "agents"), "bundled", false)
	}
	for _, item := range config.Plugins {
		if !item.Executable {
			continue
		}
		loaded, loadErrors := InspectPlugin(item)
		errors = append(errors, loadErrors...)
		for _, definition := range loaded {
			definition.Name = item.Name + ":" + definition.Name
			definition.Scope = "plugin"
			if _, found := byName[definition.Name]; !found {
				definitions = append(definitions, definition)
				byName[definition.Name] = definition
			}
		}
	}
	return &Catalog{definitions: definitions, byName: byName}, errors
}

func (c *Catalog) Definitions() []Definition {
	if c == nil {
		return nil
	}
	return append([]Definition(nil), c.definitions...)
}

func (c *Catalog) ByName(name string) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	definition, ok := c.byName[name]
	return definition, ok
}

func builtinDefinitions() []Definition {
	return []Definition{
		{Name: "general-purpose", Description: "General-purpose agent for multi-step implementation and investigation.", Prompt: "Complete the delegated task autonomously. Inspect relevant evidence, use tools as needed, verify the result, and return a concise outcome to the parent agent.", Scope: "built-in", Builtin: true},
		{Name: "explore", Description: "Fast read-only agent for searching and understanding a codebase.", Prompt: "Explore the codebase without modifying it. Return concrete findings with file paths and the evidence needed by the parent agent.", Tools: []string{"read_file", "list_files", "search_files", "list_dir", "grep", "web_search", "web_fetch"}, Scope: "built-in", Builtin: true},
		{Name: "plan", Description: "Read-only agent for producing implementation plans from repository evidence.", Prompt: "Inspect the relevant code without modifying it. Produce an implementation-ready plan grounded in repository evidence, including edge cases and verification.", Tools: []string{"read_file", "list_files", "search_files", "list_dir", "grep", "web_search", "web_fetch"}, Scope: "built-in", Builtin: true},
	}
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
		loaded, loadErrors := inspectDir(dir, item.Name, "plugin")
		definitions = append(definitions, loaded...)
		errors = append(errors, loadErrors...)
	}
	return definitions, errors
}

func inspectDir(dir, pluginName, scope string) ([]Definition, []error) {
	var definitions []Definition
	var errors []error
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, []error{err}
	}
	sort.Strings(paths)
	for _, path := range paths {
		definition, err := parse(path, pluginName, scope)
		if err != nil {
			errors = append(errors, err)
			continue
		}
		definitions = append(definitions, definition)
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
	scope := ""
	if pluginName != "" {
		scope = "plugin"
	}
	return parse(path, pluginName, scope)
}

func parse(path, pluginName, scope string) (Definition, error) {
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
		Scope: scope, Model: strings.TrimSpace(metadata.Model), Effort: strings.TrimSpace(metadata.Effort),
		PermissionMode: strings.TrimSpace(metadata.PermissionMode), Isolation: strings.TrimSpace(metadata.Isolation),
		Background: metadata.Background, InitialPrompt: strings.TrimSpace(metadata.InitialPrompt),
	}, nil
}
