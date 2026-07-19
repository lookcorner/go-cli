package marketplace

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/plugin"
)

const (
	OfficialSourceName = "xAI Official"
	OfficialSourceGit  = "https://github.com/xai-org/plugin-marketplace.git"
)

type Source struct {
	Name   string
	Path   string
	Git    string
	Branch string
}

type Entry struct {
	Name             string   `json:"name"`
	Version          string   `json:"version,omitempty"`
	Description      string   `json:"description,omitempty"`
	Category         string   `json:"category,omitempty"`
	Author           string   `json:"author,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	RelativePath     string   `json:"relativePath"`
	SkillCount       int      `json:"skillCount"`
	HasHooks         bool     `json:"hasHooks"`
	HasAgents        bool     `json:"hasAgents"`
	HasMCP           bool     `json:"hasMcp"`
	InstallStatus    string   `json:"installStatus"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	RemoteURL        string   `json:"remoteUrl,omitempty"`
	RemoteRef        string   `json:"remoteRef,omitempty"`
	RemoteSHA        string   `json:"remoteSha,omitempty"`
	RemoteSubdir     string   `json:"remoteSubdir,omitempty"`
}

type ScanResult struct {
	SourceName      string  `json:"sourceName"`
	SourceKind      string  `json:"sourceKind"`
	SourceURLOrPath string  `json:"sourceUrlOrPath"`
	Plugins         []Entry `json:"plugins"`
	Error           string  `json:"error,omitempty"`
}

type Action struct {
	Type               string
	SourceURLOrPath    string
	PluginRelativePath string
}

type Outcome struct {
	Status          string   `json:"status"`
	Message         string   `json:"message"`
	RequiresReload  bool     `json:"requiresReload"`
	RequiresRestart bool     `json:"requiresRestart"`
	Plugins         []string `json:"-"`
	RemovedPlugins  []string `json:"-"`
}

func Sources(configPath, cwd string) ([]Source, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	result := make([]Source, 0, len(cfg.Marketplace.Sources))
	for _, item := range cfg.Marketplace.Sources {
		if item.Name == "" || item.Path == "" && item.Git == "" {
			continue
		}
		path := resolveSourcePath(item.Path, cwd)
		result = append(result, Source{Name: item.Name, Path: path, Git: item.Git, Branch: item.Branch})
	}
	result = append(result, extraSources(result, marketplaceRoots())...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// AutoRegisterOfficial applies the reference implementation's default-off
// environment gate and records a sticky flag so a removed source stays removed.
func AutoRegisterOfficial(configPath string) error {
	if enabled, ok := boolEnv("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER"); !ok || !enabled {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil || cfg.Marketplace.OfficialMarketplaceAutoInstalled {
		return err
	}
	existing := make([]Source, 0, len(cfg.Marketplace.Sources))
	for _, item := range cfg.Marketplace.Sources {
		existing = append(existing, Source{Name: item.Name, Path: item.Path, Git: item.Git, Branch: item.Branch})
	}
	for _, source := range append(existing, extraSources(existing, marketplaceRoots()[:1])...) {
		if isOfficialSource(source.Git) {
			return config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
				settings.OfficialMarketplaceAutoInstalled = true
			})
		}
	}
	return config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		settings.Sources = append(settings.Sources, config.MarketplaceSourceConfig{Name: OfficialSourceName, Git: OfficialSourceGit})
		settings.OfficialMarketplaceAutoInstalled = true
	})
}

type settingsEntry struct {
	Source struct {
		Kind string `json:"source"`
		URL  string `json:"url"`
		Repo string `json:"repo"`
		Path string `json:"path"`
	} `json:"source"`
}

func extraSources(existing []Source, roots []string) []Source {
	seen := make(map[string]bool)
	for _, source := range existing {
		if source.Git != "" {
			seen[source.Git] = true
		}
	}
	var result []Source
	load := func(path string, nested bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			return
		}
		if nested {
			if json.Unmarshal(raw["extraKnownMarketplaces"], &raw) != nil {
				return
			}
		}
		names := make([]string, 0, len(raw))
		for name := range raw {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			var entry settingsEntry
			if json.Unmarshal(raw[name], &entry) != nil {
				continue
			}
			source, ok := entry.source(name)
			if !ok || source.Git != "" && seen[source.Git] {
				continue
			}
			if source.Git != "" {
				seen[source.Git] = true
			}
			result = append(result, source)
		}
	}
	for _, root := range roots {
		load(filepath.Join(root, "settings.local.json"), true)
		load(filepath.Join(root, "settings.json"), true)
	}
	for _, root := range roots {
		load(filepath.Join(root, "plugins", "known_marketplaces.json"), false)
	}
	return result
}

func removeExtraSource(identity string) (bool, error) {
	removed := false
	remove := func(path string, nested bool) error {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var document map[string]json.RawMessage
		if json.Unmarshal(data, &document) != nil {
			return nil
		}
		entries := document
		if nested {
			entries = nil
			if json.Unmarshal(document["extraKnownMarketplaces"], &entries) != nil {
				return nil
			}
		}
		changed := false
		for name, raw := range entries {
			var entry settingsEntry
			if json.Unmarshal(raw, &entry) != nil {
				continue
			}
			source, ok := entry.source(name)
			if ok && (sourceIdentity(source) == identity || source.Git != "" && sameGit(source.Git, identity)) {
				delete(entries, name)
				changed = true
			}
		}
		if !changed {
			return nil
		}
		if nested {
			raw, err := json.Marshal(entries)
			if err != nil {
				return err
			}
			document["extraKnownMarketplaces"] = raw
		}
		updated, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return err
		}
		removed = true
		return os.WriteFile(path, append(updated, '\n'), 0o600)
	}
	for _, root := range marketplaceRoots() {
		for _, name := range []string{"settings.local.json", "settings.json"} {
			if err := remove(filepath.Join(root, name), true); err != nil {
				return removed, err
			}
		}
		if err := remove(filepath.Join(root, "plugins", "known_marketplaces.json"), false); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (entry settingsEntry) source(name string) (Source, bool) {
	switch entry.Source.Kind {
	case "git":
		return Source{Name: name, Git: entry.Source.URL}, entry.Source.URL != ""
	case "github":
		return Source{Name: name, Git: "https://github.com/" + entry.Source.Repo + ".git"}, entry.Source.Repo != ""
	case "local":
		return Source{Name: name, Path: resolveSourcePath(entry.Source.Path, "")}, entry.Source.Path != ""
	default:
		return Source{}, false
	}
}

func marketplaceRoots() []string {
	grok, _ := marketplaceHome()
	roots := []string{grok}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".claude"))
	}
	return roots
}

func boolEnv(name string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on", "enabled":
		return true, true
	case "0", "false", "no", "off", "disabled":
		return false, true
	default:
		return false, false
	}
}

func isOfficialSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(strings.TrimSuffix(value, "/"), ".git")
	for _, prefix := range []string{"https://", "http://", "ssh://", "git@"} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.TrimPrefix(value, "www.")
	ownerRepo := strings.TrimPrefix(value, "github.com/")
	if ownerRepo == value {
		ownerRepo = strings.TrimPrefix(value, "github.com:")
	}
	return ownerRepo == "xai-org/plugin-marketplace"
}

func List(configPath, cwd string) ([]ScanResult, error) {
	sources, err := Sources(configPath, cwd)
	if err != nil {
		return nil, err
	}
	results := make([]ScanResult, 0, len(sources))
	for _, source := range sources {
		root, err := sourceRoot(source, false)
		result := ScanResult{SourceName: source.Name, SourceKind: sourceKind(source), SourceURLOrPath: sourceIdentity(source)}
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.Plugins = scanRoot(root, sourceIdentity(source))
		results = append(results, result)
	}
	return results, nil
}

func Execute(configPath, cwd string, action Action) (Outcome, error) {
	sources, err := Sources(configPath, cwd)
	if err != nil {
		return Outcome{}, err
	}
	switch action.Type {
	case "refresh":
		for _, source := range sources {
			if action.SourceURLOrPath != "" && sourceIdentity(source) != action.SourceURLOrPath {
				continue
			}
			if _, err := sourceRoot(source, true); err != nil {
				return Outcome{Status: "internal_error", Message: err.Error()}, nil
			}
		}
		return Outcome{Status: "success", Message: "Marketplace sources refreshed."}, nil
	case "add_source":
		return addSource(configPath, cwd, sources, action), nil
	case "remove_source":
		return removeSource(configPath, cwd, action), nil
	case "install":
		return installEntry(configPath, cwd, sources, action)
	case "update":
		return updateEntry(sources, action)
	case "uninstall":
		return uninstallEntry(sources, action)
	default:
		return Outcome{Status: "validation_error", Message: "Unsupported marketplace action."}, nil
	}
}

func installEntry(configPath, cwd string, sources []Source, action Action) (Outcome, error) {
	source, root, entry, err := resolveEntry(sources, action)
	if err != nil {
		return Outcome{Status: "not_found", Message: err.Error()}, nil
	}
	installSource, err := entryInstallSource(root, entry, action.PluginRelativePath)
	if err != nil {
		return Outcome{Status: "validation_error", Message: err.Error()}, nil
	}
	installed, err := plugin.Install(installSource, cwd)
	if err != nil {
		return Outcome{Status: "internal_error", Message: "Install failed: " + err.Error()}, nil
	}
	if err := plugin.SetMarketplace(installed.RepoKey, installed.Plugins[0], plugin.MarketplaceProvenance{
		SourceURLOrPath: sourceIdentity(source), SourceDisplayName: source.Name, PluginSubdir: action.PluginRelativePath,
	}); err != nil {
		_, _ = plugin.Uninstall(installed.Plugins[0], true, false)
		return Outcome{Status: "internal_error", Message: err.Error()}, nil
	}
	return Outcome{Status: "success", Message: fmt.Sprintf("Installed %s from %s.", action.PluginRelativePath, source.Name), Plugins: installed.Plugins}, nil
}

func updateEntry(sources []Source, action Action) (Outcome, error) {
	key, _, ok, err := plugin.FindMarketplace(action.SourceURLOrPath, action.PluginRelativePath)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Status: "not_found", Message: "Marketplace plugin is not installed."}, nil
	}
	source, root, entry, resolveErr := resolveEntry(sources, action)
	if resolveErr != nil {
		return Outcome{Status: "not_found", Message: "Marketplace plugin is not found in the source."}, nil
	}
	installSource, err := entryInstallSource(root, entry, action.PluginRelativePath)
	if err != nil {
		return Outcome{Status: "validation_error", Message: err.Error()}, nil
	}
	replaced, err := plugin.ReplaceMarketplace(key, installSource, root, plugin.MarketplaceProvenance{
		SourceURLOrPath: sourceIdentity(source), SourceDisplayName: source.Name, PluginSubdir: action.PluginRelativePath,
	})
	if err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}, nil
	}
	return Outcome{
		Status: "success", Message: fmt.Sprintf("Updated %s.", action.PluginRelativePath),
		Plugins: replaced.Plugins, RemovedPlugins: replaced.PreviousPlugins,
	}, nil
}

func uninstallEntry(_ []Source, action Action) (Outcome, error) {
	_, name, ok, err := plugin.FindMarketplace(action.SourceURLOrPath, action.PluginRelativePath)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{Status: "not_found", Message: "Marketplace plugin is not installed."}, nil
	}
	removed, err := plugin.Uninstall(name, true, false)
	if err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}, nil
	}
	return Outcome{Status: "success", Message: fmt.Sprintf("Uninstalled %s.", action.PluginRelativePath), Plugins: removed.Plugins}, nil
}

func resolveEntry(sources []Source, action Action) (Source, string, Entry, error) {
	for _, source := range sources {
		if sourceIdentity(source) != action.SourceURLOrPath {
			continue
		}
		root, err := sourceRoot(source, false)
		if err != nil {
			return Source{}, "", Entry{}, err
		}
		for _, entry := range scanRoot(root, sourceIdentity(source)) {
			if entry.RelativePath == action.PluginRelativePath {
				return source, root, entry, nil
			}
		}
		return Source{}, "", Entry{}, fmt.Errorf("marketplace plugin %q not found", action.PluginRelativePath)
	}
	return Source{}, "", Entry{}, fmt.Errorf("marketplace source %q not found", action.SourceURLOrPath)
}

func entryInstallSource(root string, entry Entry, relativePath string) (string, error) {
	if entry.RemoteURL != "" {
		source := entry.RemoteURL
		if entry.RemoteSHA != "" {
			source += "@" + entry.RemoteSHA
		} else if entry.RemoteRef != "" {
			source += "@" + entry.RemoteRef
		}
		if entry.RemoteSubdir != "" {
			source += "#" + entry.RemoteSubdir
		}
		return source, nil
	}
	return safeJoin(root, relativePath)
}

func addSource(configPath, cwd string, sources []Source, action Action) Outcome {
	if strings.TrimSpace(action.SourceURLOrPath) == "" {
		return Outcome{Status: "validation_error", Message: "Marketplace source is required."}
	}
	path, git := normalizeSource(action.SourceURLOrPath, cwd)
	if path != "" {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return Outcome{Status: "validation_error", Message: "Local marketplace path not found (or is not a directory): " + path}
		}
	}
	for _, source := range sources {
		if path != "" && source.Path == path || git != "" && sameGit(source.Git, git) {
			return Outcome{Status: "validation_error", Message: "Marketplace source already configured: " + action.SourceURLOrPath}
		}
	}
	value := path
	if git != "" {
		value = git
	}
	entry := config.MarketplaceSourceConfig{Name: sourceName(value)}
	if git != "" {
		entry.Git = git
	} else {
		entry.Path = path
	}
	if err := config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		for _, existing := range settings.Sources {
			if entry.Path != "" && existing.Path == entry.Path || entry.Git != "" && existing.Git == entry.Git {
				return
			}
		}
		settings.Sources = append(settings.Sources, entry)
		if isOfficialSource(entry.Git) {
			settings.OfficialMarketplaceAutoInstalled = true
		}
	}); err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}
	}
	return Outcome{Status: "success", Message: "Marketplace source added."}
}

func removeSource(configPath, cwd string, action Action) Outcome {
	path, git := normalizeSource(action.SourceURLOrPath, cwd)
	sources, err := Sources(configPath, cwd)
	if err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}
	}
	identity := ""
	for _, source := range sources {
		if path != "" && source.Path == path || git != "" && sameGit(source.Git, git) {
			identity = sourceIdentity(source)
			break
		}
	}
	if identity == "" {
		return Outcome{Status: "not_found", Message: fmt.Sprintf("Marketplace source %q not found.", action.SourceURLOrPath)}
	}
	removed, err := uninstallSourcePlugins(identity)
	if err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}
	}
	if err := config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		filtered := settings.Sources[:0]
		for _, item := range settings.Sources {
			if resolveSourcePath(item.Path, cwd) != identity && item.Git != identity {
				filtered = append(filtered, item)
			}
		}
		settings.Sources = filtered
		if isOfficialSource(identity) {
			settings.OfficialMarketplaceAutoInstalled = true
		}
	}); err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}
	}
	if _, err := removeExtraSource(identity); err != nil {
		return Outcome{Status: "internal_error", Message: err.Error()}
	}
	if len(removed) > 0 {
		if err := config.UpdatePlugins(configPath, func(settings *config.PluginsConfig) {
			for _, name := range removed {
				settings.Enabled = removeString(settings.Enabled, name)
				settings.Disabled = removeString(settings.Disabled, name)
			}
		}); err != nil {
			return Outcome{Status: "internal_error", Message: err.Error()}
		}
	}
	message := "Marketplace source removed."
	if len(removed) > 0 {
		message = fmt.Sprintf("Marketplace source removed and uninstalled %d plugin(s): %s", len(removed), strings.Join(removed, ", "))
	}
	return Outcome{Status: "success", Message: message, Plugins: removed}
}

func normalizeSource(value, cwd string) (path, git string) {
	value = strings.TrimSpace(value)
	local := filepath.IsAbs(value) || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") || strings.HasPrefix(value, "\\") || len(value) >= 3 && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
	if local {
		return resolveSourcePath(value, cwd), ""
	}
	if !strings.Contains(value, "://") && !strings.Contains(value, "git@") {
		value = "https://github.com/" + strings.TrimSuffix(value, ".git") + ".git"
	}
	return "", value
}

func sameGit(left, right string) bool {
	return strings.TrimSuffix(left, ".git") == strings.TrimSuffix(right, ".git")
}

func uninstallSourcePlugins(identity string) ([]string, error) {
	registry, err := plugin.LoadInstallRegistry()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, repo := range registry.Repos {
		if repo.Marketplace == nil || repo.Marketplace.SourceURLOrPath != identity {
			continue
		}
		for name := range repo.Plugins {
			names = append(names, name)
			break
		}
	}
	sort.Strings(names)
	var removed []string
	for _, name := range names {
		outcome, err := plugin.Uninstall(name, true, false)
		if err != nil {
			return removed, err
		}
		removed = append(removed, outcome.Plugins...)
	}
	sort.Strings(removed)
	return removed, nil
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func sourceIdentity(source Source) string {
	if source.Git != "" {
		return source.Git
	}
	return source.Path
}

func sourceKind(source Source) string {
	if source.Git != "" {
		return "git"
	}
	return "local"
}

func sourceRoot(source Source, force bool) (string, error) {
	if source.Git != "" {
		home, err := marketplaceHome()
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256([]byte(source.Git + "\x00" + source.Branch))
		cache := filepath.Join(home, "marketplace-cache", fmt.Sprintf("%x", digest[:4]))
		marker := filepath.Join(cache, ".gork-marketplace-sync")
		if !force {
			if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 5*time.Minute {
				return cache, nil
			}
		}
		if !isDir(cache) {
			args := []string{"clone", "--depth", "1"}
			if source.Branch != "" {
				args = append(args, "--branch", source.Branch)
			}
			args = append(args, "--", source.Git, cache)
			if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return "", fmt.Errorf("git marketplace clone failed: %s", strings.TrimSpace(string(output)))
			}
		} else {
			command := exec.Command("git", "pull", "--ff-only")
			command.Dir = cache
			if output, err := command.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git marketplace refresh failed: %s", strings.TrimSpace(string(output)))
			}
		}
		_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
		return cache, nil
	}
	if !isDir(source.Path) {
		return "", fmt.Errorf("marketplace path %q is not a directory", source.Path)
	}
	return source.Path, nil
}

type indexFile struct {
	Plugins []indexEntry `json:"plugins"`
}

type indexEntry struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Author      json.RawMessage `json:"author"`
	Tags        []string        `json:"tags"`
	Source      json.RawMessage `json:"source"`
}

func scanRoot(root, sourceIdentity string) []Entry {
	registry, _ := plugin.LoadInstallRegistry()
	for _, indexPath := range []string{
		filepath.Join(root, ".grok-plugin", "marketplace.json"),
		filepath.Join(root, ".claude-plugin", "marketplace.json"),
	} {
		data, err := os.ReadFile(indexPath)
		if err != nil {
			continue
		}
		var index indexFile
		if json.Unmarshal(data, &index) == nil && len(index.Plugins) > 0 {
			entries := make([]Entry, 0, len(index.Plugins))
			for _, item := range index.Plugins {
				path := ""
				var source struct {
					Path string `json:"path"`
					URL  string `json:"url"`
					Ref  string `json:"ref"`
					SHA  string `json:"sha"`
				}
				if len(item.Source) > 0 {
					var direct string
					if json.Unmarshal(item.Source, &direct) == nil {
						source.Path = direct
					} else {
						_ = json.Unmarshal(item.Source, &source)
					}
					path = source.Path
				}
				entry := Entry{Name: item.Name, Version: item.Version, Description: item.Description, Category: item.Category, Author: indexAuthor(item.Author), Tags: item.Tags, RelativePath: path}
				if entry.RelativePath == "" {
					if source.URL != "" {
						entry.RelativePath = item.Name
					} else {
						entry.RelativePath = "plugins/" + item.Name
					}
				}
				entry.RemoteURL, entry.RemoteRef, entry.RemoteSHA = source.URL, source.Ref, source.SHA
				if source.URL != "" {
					entry.RemoteSubdir = source.Path
				}
				if entry.RemoteURL == "" {
					pluginRoot, err := safeJoin(root, entry.RelativePath)
					if err != nil || !isDir(pluginRoot) {
						continue
					}
					enrichEntry(&entry, pluginRoot)
				}
				entries = append(entries, withInstallStatus(entry, sourceIdentity, registry))
			}
			return entries
		}
	}
	pluginsRoot := filepath.Join(root, "plugins")
	entries, _ := os.ReadDir(pluginsRoot)
	result := make([]Entry, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir() || strings.HasPrefix(item.Name(), ".") {
			continue
		}
		entry := Entry{Name: item.Name(), RelativePath: filepath.ToSlash(filepath.Join("plugins", item.Name()))}
		enrichEntry(&entry, filepath.Join(pluginsRoot, item.Name()))
		result = append(result, withInstallStatus(entry, sourceIdentity, registry))
	}
	defaultSkills := filepath.Join(root, "default-skills")
	if count := countSkills(defaultSkills); count > 0 {
		result = append(result, withInstallStatus(Entry{
			Name: "default-skills", RelativePath: "default-skills", SkillCount: count,
		}, sourceIdentity, registry))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RelativePath < result[j].RelativePath })
	return result
}

func enrichEntry(entry *Entry, root string) {
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if data, err := os.ReadFile(filepath.Join(root, "plugin.json")); err == nil && json.Unmarshal(data, &manifest) == nil {
		if manifest.Name != "" {
			entry.Name = manifest.Name
		}
		entry.Version, entry.Description = manifest.Version, manifest.Description
	}
	entry.SkillCount = countSkills(filepath.Join(root, "skills"))
	entry.HasHooks = isFile(filepath.Join(root, "hooks", "hooks.json"))
	entry.HasAgents = isDir(filepath.Join(root, "agents"))
	entry.HasMCP = isFile(filepath.Join(root, ".mcp.json"))
}

func withInstallStatus(entry Entry, sourceIdentity string, registry *plugin.InstallRegistry) Entry {
	if registry == nil {
		return entry
	}
	for _, repo := range registry.Repos {
		if repo.Marketplace != nil && repo.Marketplace.SourceURLOrPath == sourceIdentity && repo.Marketplace.PluginSubdir == entry.RelativePath {
			entry.InstallStatus = "installed"
			for _, installed := range repo.Plugins {
				entry.InstalledVersion = installed.Version
				break
			}
			return entry
		}
	}
	entry.InstallStatus = "not_installed"
	return entry
}

func safeJoin(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("marketplace plugin path is invalid")
	}
	candidate := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	if !pathWithin(root, candidate) {
		return "", errors.New("marketplace plugin path escapes source")
	}
	return candidate, nil
}

func countSkills(root string) int {
	entries, _ := os.ReadDir(root)
	count := 0
	for _, item := range entries {
		if item.IsDir() && isFile(filepath.Join(root, item.Name(), "SKILL.md")) {
			count++
		}
	}
	return count
}

func isDir(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sourceName(value string) string {
	value = strings.TrimSuffix(strings.TrimRight(value, "/"), ".git")
	if index := strings.LastIndexAny(value, "/:"); index >= 0 {
		value = value[index+1:]
	}
	if value == "" {
		return "marketplace"
	}
	return value
}

func resolveSourcePath(path, cwd string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func marketplaceHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Clean(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok"), nil
}

func indexAuthor(raw json.RawMessage) string {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var author struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &author)
	return author.Name
}
