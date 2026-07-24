package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type ComponentSummary struct {
	SkillDirs   int
	CommandDirs int
	AgentDirs   int
	Hooks       bool
	MCP         bool
	LSP         bool
}

type InstalledPluginDetails struct {
	RepoKey     string
	Path        string
	Kind        InstallKind
	InstalledAt string
	UpdatedAt   string
	Plugins     []RepoPluginDetails
	Description string
	Components  ComponentSummary
}

type RepoPluginDetails struct {
	Name    string
	Version string
	Subdir  string
}

type ManifestValidation struct {
	Found       bool
	Name        string
	Version     string
	Description string
	Components  ComponentSummary
}

type ListEntry struct {
	Name        string
	RepoKey     string
	Version     string
	Path        string
	Source      string
	Marketplace string
}

func ListInstalled() ([]ListEntry, error) {
	registry, err := LoadInstallRegistry()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(registry.Repos))
	for key := range registry.Repos {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var entries []ListEntry
	for _, key := range keys {
		repo := registry.Repos[key]
		source := repo.Kind.URL
		if repo.Kind.Type == "local" {
			source = repo.Kind.SourcePath
		}
		marketplace := ""
		if repo.Marketplace != nil {
			marketplace = repo.Marketplace.SourceDisplayName
		}
		for _, name := range sortedPluginNames(repo.Plugins) {
			entries = append(entries, ListEntry{
				Name: name, RepoKey: key, Version: repo.Plugins[name].Version,
				Path: repo.Path, Source: source, Marketplace: marketplace,
			})
		}
	}
	return entries, nil
}

func InstalledDetails(name string) (InstalledPluginDetails, error) {
	registry, err := LoadInstallRegistry()
	if err != nil {
		return InstalledPluginDetails{}, err
	}
	key, repo, ok := registry.findPlugin(pluginName(name))
	if !ok {
		return InstalledPluginDetails{}, &NotFoundError{Plugin: pluginName(name)}
	}
	names := sortedPluginNames(repo.Plugins)
	details := InstalledPluginDetails{
		RepoKey: key, Path: repo.Path, Kind: repo.Kind,
		InstalledAt: repo.InstalledAt, UpdatedAt: repo.UpdatedAt,
		Plugins: make([]RepoPluginDetails, 0, len(names)),
	}
	for _, pluginName := range names {
		item := repo.Plugins[pluginName]
		details.Plugins = append(details.Plugins, RepoPluginDetails{Name: pluginName, Version: item.Version, Subdir: item.Subdir})
	}
	selected := repo.Plugins[pluginName(name)]
	root := repo.Path
	if selected.Subdir != "" {
		root = filepath.Join(root, filepath.FromSlash(selected.Subdir))
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if manifest, found, loadErr := loadManifestStrict(root); loadErr == nil && found {
		details.Description = manifest.Description
		details.Components = summarizeComponents(root, manifest)
	}
	return details, nil
}

func Validate(path string) (ManifestValidation, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ManifestValidation{}, fmt.Errorf("not a directory: %s", path)
		}
		return ManifestValidation{}, err
	}
	if !info.IsDir() {
		return ManifestValidation{}, fmt.Errorf("not a directory: %s", path)
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return ManifestValidation{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ManifestValidation{}, err
	}
	manifest, found, err := loadManifestStrict(root)
	if err != nil {
		return ManifestValidation{}, err
	}
	if !found {
		return ManifestValidation{}, nil
	}
	return ManifestValidation{
		Found: true, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description,
		Components: summarizeComponents(root, manifest),
	}, nil
}

func loadManifestStrict(root string) (manifest, bool, error) {
	for _, relative := range []string{"plugin.json", filepath.Join(".grok-plugin", "plugin.json"), filepath.Join(".claude-plugin", "plugin.json")} {
		path := filepath.Join(root, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return manifest{}, false, fmt.Errorf("read plugin manifest %q: %w", path, err)
		}
		var value manifest
		if err := json.Unmarshal(data, &value); err != nil {
			return manifest{}, false, fmt.Errorf("parse plugin manifest %q: %w", path, err)
		}
		if !validName(value.Name) {
			return manifest{}, false, fmt.Errorf("manifest validation failed: invalid plugin name %q", value.Name)
		}
		return value, true, nil
	}
	return manifest{}, false, nil
}

func summarizeComponents(root string, value manifest) ComponentSummary {
	return ComponentSummary{
		SkillDirs: len(resolveDirs(root, value.Skills, "skills")), CommandDirs: len(resolveDirs(root, value.Commands, "commands")),
		AgentDirs: len(resolveDirs(root, value.Agents, "agents")),
		Hooks:     len(value.Hooks.Inline) > 0 || resolveHooksConfig(root, value.Hooks) != "",
		MCP:       len(value.MCPServers.Inline) > 0 || resolveMCPConfig(root, value.MCPServers) != "",
		LSP:       len(value.LSPServers.Inline) > 0 || resolveLSPConfig(root, value.LSPServers) != "",
	}
}
