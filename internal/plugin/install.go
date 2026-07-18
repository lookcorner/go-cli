package plugin

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var installMu sync.Mutex

type InstallKind struct {
	Type       string `json:"type"`
	URL        string `json:"url,omitempty"`
	Ref        string `json:"git_ref,omitempty"`
	Commit     string `json:"commit,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	Subdir     string `json:"subdir,omitempty"`
}

type RepoPlugin struct {
	Subdir  string `json:"subdir,omitempty"`
	Version string `json:"version,omitempty"`
}

type InstalledRepo struct {
	Kind        InstallKind            `json:"kind"`
	InstalledAt string                 `json:"installed_at"`
	UpdatedAt   string                 `json:"updated_at"`
	Path        string                 `json:"path"`
	Plugins     map[string]RepoPlugin  `json:"plugins"`
	Marketplace *MarketplaceProvenance `json:"marketplace,omitempty"`
}

type MarketplaceProvenance struct {
	SourceURLOrPath   string `json:"source_url_or_path"`
	SourceDisplayName string `json:"source_display_name"`
	PluginSubdir      string `json:"plugin_subdir"`
}

type InstallRegistry struct {
	Version int                      `json:"version"`
	Repos   map[string]InstalledRepo `json:"repos"`
	dir     string
}

type InstallOutcome struct {
	RepoKey         string
	Plugins         []string
	PreviousPlugins []string
}

type UninstallOutcome struct {
	RepoKey string
	Plugins []string
}

type ConfirmationError struct {
	RepoKey string
	Plugins []string
}

type NotFoundError struct{ Plugin string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("plugin %q not found in install registry", e.Plugin)
}

func (e *ConfirmationError) Error() string {
	return fmt.Sprintf("repo %q contains %d plugins: %s", e.RepoKey, len(e.Plugins), strings.Join(e.Plugins, ", "))
}

type UpdateOutcome struct {
	RepoKey string
	Status  string
}

type installSource struct {
	kind   string
	value  string
	ref    string
	subdir string
}

func Install(source, cwd string) (InstallOutcome, error) {
	installMu.Lock()
	defer installMu.Unlock()
	parsed, err := parseInstallSource(source, cwd)
	if err != nil {
		return InstallOutcome{}, err
	}
	registry, err := LoadInstallRegistry()
	if err != nil {
		return InstallOutcome{}, err
	}
	key := repoKey(parsed.identity())
	if _, exists := registry.Repos[key]; exists {
		return InstallOutcome{}, fmt.Errorf("plugin source is already installed as %q", key)
	}
	if err := os.MkdirAll(registry.dir, 0o700); err != nil {
		return InstallOutcome{}, fmt.Errorf("create plugin install directory: %w", err)
	}
	target := filepath.Join(registry.dir, key)
	if parsed.kind == "local" && pathWithin(parsed.value, target) {
		return InstallOutcome{}, errors.New("plugin install directory is inside the local source")
	}
	if parsed.kind == "git" {
		err = clonePluginRepo(parsed, target)
	} else {
		err = copyPluginDir(parsed.value, target)
	}
	if err != nil {
		_ = os.RemoveAll(target)
		return InstallOutcome{}, err
	}
	discovered, err := discoverInstalledPlugins(target, parsed.subdir)
	if err != nil || len(discovered) == 0 {
		_ = os.RemoveAll(target)
		if err == nil {
			err = errors.New("no plugins found in source")
		}
		return InstallOutcome{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	kind := InstallKind{Type: parsed.kind, Subdir: parsed.subdir}
	if parsed.kind == "git" {
		kind.URL, kind.Ref = parsed.value, parsed.ref
		kind.Commit, _ = gitOutput(target, "rev-parse", "HEAD")
	} else {
		kind.SourcePath = parsed.value
	}
	registry.Repos[key] = InstalledRepo{
		Kind: kind, InstalledAt: now, UpdatedAt: now, Path: target, Plugins: discovered,
	}
	if err := registry.Save(); err != nil {
		delete(registry.Repos, key)
		_ = os.RemoveAll(target)
		return InstallOutcome{}, err
	}
	return InstallOutcome{RepoKey: key, Plugins: sortedPluginNames(discovered)}, nil
}

func Uninstall(pluginID string, confirmed, keepData bool) (UninstallOutcome, error) {
	installMu.Lock()
	defer installMu.Unlock()
	registry, err := LoadInstallRegistry()
	if err != nil {
		return UninstallOutcome{}, err
	}
	name := pluginName(pluginID)
	key, repo, ok := registry.findPlugin(name)
	if !ok {
		return UninstallOutcome{}, &NotFoundError{Plugin: name}
	}
	names := sortedPluginNames(repo.Plugins)
	if len(names) > 1 && !confirmed {
		return UninstallOutcome{}, &ConfirmationError{RepoKey: key, Plugins: names}
	}
	if err := removeInstalledPath(repo.Path, registry.dir); err != nil {
		return UninstallOutcome{}, err
	}
	if !keepData {
		cleanupPluginData(repo, filepath.Dir(registry.dir))
	}
	delete(registry.Repos, key)
	if err := registry.Save(); err != nil {
		return UninstallOutcome{}, err
	}
	return UninstallOutcome{RepoKey: key, Plugins: names}, nil
}

func Update(pluginID string) ([]UpdateOutcome, error) {
	installMu.Lock()
	defer installMu.Unlock()
	registry, err := LoadInstallRegistry()
	if err != nil {
		return nil, err
	}
	var keys []string
	if strings.TrimSpace(pluginID) != "" {
		key, _, ok := registry.findPlugin(pluginName(pluginID))
		if !ok {
			return nil, &NotFoundError{Plugin: pluginName(pluginID)}
		}
		keys = []string{key}
	} else {
		for key := range registry.Repos {
			keys = append(keys, key)
		}
		sort.Strings(keys)
	}
	if len(keys) == 0 {
		return nil, errors.New("no installed plugins to update")
	}
	outcomes := make([]UpdateOutcome, 0, len(keys))
	for _, key := range keys {
		repo := registry.Repos[key]
		status, err := updateInstalledRepo(key, &repo, registry.dir)
		if err != nil {
			return nil, err
		}
		registry.Repos[key] = repo
		outcomes = append(outcomes, UpdateOutcome{RepoKey: key, Status: status})
	}
	if err := registry.Save(); err != nil {
		return nil, err
	}
	return outcomes, nil
}

func RefreshLocal() error {
	installMu.Lock()
	defer installMu.Unlock()
	registry, err := LoadInstallRegistry()
	if err != nil {
		return err
	}
	changed := false
	var failures error
	for key, value := range registry.Repos {
		if value.Kind.Type != "local" {
			continue
		}
		repo := value
		if _, err := refreshLocalRepo(key, &repo, registry.dir); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		registry.Repos[key] = repo
		changed = true
	}
	if changed {
		failures = errors.Join(failures, registry.Save())
	}
	return failures
}

func ReplaceMarketplace(repoKey, source, cwd string, provenance MarketplaceProvenance) (InstallOutcome, error) {
	installMu.Lock()
	defer installMu.Unlock()
	registry, err := LoadInstallRegistry()
	if err != nil {
		return InstallOutcome{}, err
	}
	previous, ok := registry.Repos[repoKey]
	if !ok || previous.Marketplace == nil {
		return InstallOutcome{}, fmt.Errorf("marketplace plugin repo %q not found", repoKey)
	}
	parsed, err := parseInstallSource(source, cwd)
	if err != nil {
		return InstallOutcome{}, err
	}
	stagingRoot, err := os.MkdirTemp(registry.dir, ".marketplace-update-*")
	if err != nil {
		return InstallOutcome{}, err
	}
	defer os.RemoveAll(stagingRoot)
	staging := filepath.Join(stagingRoot, "repo")
	if parsed.kind == "git" {
		err = clonePluginRepo(parsed, staging)
	} else {
		err = copyPluginDir(parsed.value, staging)
	}
	if err != nil {
		return InstallOutcome{}, err
	}
	discovered, err := discoverInstalledPlugins(staging, parsed.subdir)
	if err != nil || len(discovered) == 0 {
		if err == nil {
			err = errors.New("no plugins found in marketplace update")
		}
		return InstallOutcome{}, err
	}
	kind := InstallKind{Type: parsed.kind, Subdir: parsed.subdir}
	if parsed.kind == "git" {
		kind.URL, kind.Ref = parsed.value, parsed.ref
		kind.Commit, _ = gitOutput(staging, "rev-parse", "HEAD")
	} else {
		kind.SourcePath = parsed.value
	}
	backup := previous.Path + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(previous.Path, backup); err != nil {
		return InstallOutcome{}, err
	}
	if err := os.Rename(staging, previous.Path); err != nil {
		_ = os.Rename(backup, previous.Path)
		return InstallOutcome{}, err
	}
	next := InstalledRepo{
		Kind: kind, InstalledAt: previous.InstalledAt, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Path: previous.Path, Plugins: discovered, Marketplace: &provenance,
	}
	registry.Repos[repoKey] = next
	if err := registry.Save(); err != nil {
		_ = os.RemoveAll(previous.Path)
		_ = os.Rename(backup, previous.Path)
		registry.Repos[repoKey] = previous
		return InstallOutcome{}, err
	}
	_ = os.RemoveAll(backup)
	return InstallOutcome{
		RepoKey: repoKey, Plugins: sortedPluginNames(discovered), PreviousPlugins: sortedPluginNames(previous.Plugins),
	}, nil
}

func LoadInstallRegistry() (*InstallRegistry, error) {
	dir, err := installDir()
	if err != nil {
		return nil, err
	}
	return loadRegistryAt(dir)
}

func loadRegistryAt(dir string) (*InstallRegistry, error) {
	registry := &InstallRegistry{Version: 1, Repos: make(map[string]InstalledRepo), dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plugin install registry: %w", err)
	}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("parse plugin install registry: %w", err)
	}
	if registry.Repos == nil {
		registry.Repos = make(map[string]InstalledRepo)
	}
	registry.dir = dir
	return registry, nil
}

func (r *InstallRegistry) Save() error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(r.dir, ".registry-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filepath.Join(r.dir, "registry.json")); err != nil {
		return fmt.Errorf("replace plugin install registry: %w", err)
	}
	return nil
}

func (r *InstallRegistry) findPlugin(name string) (string, InstalledRepo, bool) {
	for key, repo := range r.Repos {
		if _, ok := repo.Plugins[name]; ok {
			return key, repo, true
		}
	}
	return "", InstalledRepo{}, false
}

func SetMarketplace(repoKey, pluginName string, provenance MarketplaceProvenance) error {
	installMu.Lock()
	defer installMu.Unlock()
	registry, err := LoadInstallRegistry()
	if err != nil {
		return err
	}
	repo, ok := registry.Repos[repoKey]
	if !ok {
		return fmt.Errorf("plugin repo %q not found", repoKey)
	}
	if _, ok := repo.Plugins[pluginName]; !ok {
		return fmt.Errorf("plugin %q not found in repo %q", pluginName, repoKey)
	}
	repo.Marketplace = &provenance
	registry.Repos[repoKey] = repo
	return registry.Save()
}

func FindMarketplace(source, relativePath string) (string, string, bool, error) {
	registry, err := LoadInstallRegistry()
	if err != nil {
		return "", "", false, err
	}
	for key, repo := range registry.Repos {
		if repo.Marketplace == nil || repo.Marketplace.SourceURLOrPath != source || repo.Marketplace.PluginSubdir != relativePath {
			continue
		}
		for name := range repo.Plugins {
			return key, name, true, nil
		}
	}
	return "", "", false, nil
}

func updateInstalledRepo(key string, repo *InstalledRepo, installRoot string) (string, error) {
	if repo.Kind.Type == "local" {
		return refreshLocalRepo(key, repo, installRoot)
	}
	if repo.Kind.Type != "git" {
		return "", fmt.Errorf("plugin repo %q has unknown install type %q", key, repo.Kind.Type)
	}
	if pinnedRef(repo.Kind.Ref) {
		return "pinned", nil
	}
	old := repo.Kind.Commit
	if _, err := gitOutput(repo.Path, "pull", "--ff-only"); err != nil {
		return "", fmt.Errorf("update plugin repo %q: %w", key, err)
	}
	plugins, err := discoverInstalledPlugins(repo.Path, repo.Kind.Subdir)
	if err != nil || len(plugins) == 0 {
		return "", fmt.Errorf("updated plugin repo %q contains no plugins", key)
	}
	repo.Plugins = plugins
	repo.Kind.Commit, _ = gitOutput(repo.Path, "rev-parse", "HEAD")
	repo.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if old == repo.Kind.Commit {
		return "already_up_to_date", nil
	}
	return "updated", nil
}

func refreshLocalRepo(key string, repo *InstalledRepo, installRoot string) (string, error) {
	if !pathWithin(installRoot, repo.Path) {
		return "", fmt.Errorf("installed plugin path %q escapes %q", repo.Path, installRoot)
	}
	if pathWithin(repo.Kind.SourcePath, repo.Path) {
		return "", errors.New("plugin install directory is inside the local source")
	}
	temp, err := os.MkdirTemp(installRoot, ".refresh-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	if err := copyPluginDir(repo.Kind.SourcePath, temp); err != nil {
		return "", fmt.Errorf("refresh local plugin %q: %w", key, err)
	}
	plugins, err := discoverInstalledPlugins(temp, repo.Kind.Subdir)
	if err != nil || len(plugins) == 0 {
		return "", fmt.Errorf("refreshed plugin repo %q contains no plugins", key)
	}
	backup := repo.Path + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(repo.Path, backup); err != nil {
		return "", err
	}
	if err := os.Rename(temp, repo.Path); err != nil {
		_ = os.Rename(backup, repo.Path)
		return "", err
	}
	_ = os.RemoveAll(backup)
	repo.Plugins = plugins
	repo.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return "updated", nil
}

func parseInstallSource(raw, cwd string) (installSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return installSource{}, errors.New("plugin source is required")
	}
	main, subdir := raw, ""
	if index := strings.LastIndexByte(raw, '#'); index >= 0 {
		main, subdir = raw[:index], raw[index+1:]
		if err := validateSubdir(subdir); err != nil {
			return installSource{}, err
		}
	}
	if strings.Contains(main, "://") || strings.HasPrefix(main, "git@") || githubShorthand(main) {
		url, ref := splitGitRef(main)
		if githubShorthand(url) {
			url = "https://github.com/" + url
		}
		return installSource{kind: "git", value: url, ref: ref, subdir: subdir}, nil
	}
	path := ResolvePath(main, cwd)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return installSource{}, fmt.Errorf("local plugin source %q is not a directory", path)
	}
	return installSource{kind: "local", value: path, subdir: subdir}, nil
}

func (s installSource) identity() string {
	if s.subdir != "" {
		return s.value + "#" + s.subdir
	}
	return s.value
}

func splitGitRef(source string) (string, string) {
	start := 0
	if strings.HasPrefix(source, "git@") {
		start = strings.IndexByte(source, ':') + 1
	}
	if index := strings.LastIndexByte(source[start:], '@'); index >= 0 {
		index += start
		return source[:index], source[index+1:]
	}
	return source, ""
}

func githubShorthand(source string) bool {
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~") {
		return false
	}
	base, _ := splitGitRef(source)
	parts := strings.Split(base, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func validateSubdir(subdir string) error {
	if subdir == "" || filepath.IsAbs(subdir) {
		return errors.New("plugin subdirectory is invalid")
	}
	for _, part := range strings.Split(filepath.ToSlash(subdir), "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("plugin subdirectory escapes the source")
		}
	}
	return nil
}

func clonePluginRepo(source installSource, target string) error {
	if len(source.ref) == 40 && allHex(source.ref) {
		if output, err := exec.Command("git", "init", target).CombinedOutput(); err != nil {
			return fmt.Errorf("git init failed: %s: %w", strings.TrimSpace(string(output)), err)
		}
		if _, err := gitOutput(target, "remote", "add", "origin", source.value); err != nil {
			return err
		}
		if _, err := gitOutput(target, "fetch", "--depth", "1", "origin", source.ref); err != nil {
			return err
		}
		if _, err := gitOutput(target, "checkout", "--detach", "FETCH_HEAD"); err != nil {
			return err
		}
		return nil
	}
	args := []string{"clone", "--depth", "1"}
	if source.ref != "" {
		args = append(args, "--branch", source.ref)
	}
	args = append(args, "--", source.value, target)
	command := exec.Command("git", args...)
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func copyPluginDir(source, target string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("plugin source %q is not a directory", source)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := copyPluginDir(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err == nil {
			_, err = io.Copy(output, input)
		}
		input.Close()
		if output != nil {
			err = errors.Join(err, output.Close())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func discoverInstalledPlugins(root, subdir string) (map[string]RepoPlugin, error) {
	scanRoot := root
	if subdir != "" {
		if err := validateSubdir(subdir); err != nil {
			return nil, err
		}
		scanRoot = filepath.Join(root, filepath.FromSlash(subdir))
		if !isDir(scanRoot) {
			return nil, fmt.Errorf("plugin subdirectory %q does not exist", subdir)
		}
	}
	if item, ok := installedPlugin(scanRoot, subdir); ok {
		return map[string]RepoPlugin{item.name: item.plugin}, nil
	}
	entries, err := os.ReadDir(scanRoot)
	if err != nil {
		return nil, err
	}
	plugins := make(map[string]RepoPlugin)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		relative := entry.Name()
		if subdir != "" {
			relative = filepath.ToSlash(filepath.Join(subdir, relative))
		}
		if item, ok := installedPlugin(filepath.Join(scanRoot, entry.Name()), relative); ok {
			if _, duplicate := plugins[item.name]; duplicate {
				return nil, fmt.Errorf("plugin %q is duplicated in source", item.name)
			}
			plugins[item.name] = item.plugin
		}
	}
	return plugins, nil
}

func installedPlugin(root, subdir string) (struct {
	name   string
	plugin RepoPlugin
}, bool) {
	m, ok := loadManifest(root)
	if !ok {
		return struct {
			name   string
			plugin RepoPlugin
		}{}, false
	}
	return struct {
		name   string
		plugin RepoPlugin
	}{m.Name, RepoPlugin{Subdir: subdir, Version: m.Version}}, true
}

func installDir() (string, error) {
	grokHome := strings.TrimSpace(os.Getenv("GROK_HOME"))
	if grokHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot resolve plugin install directory")
		}
		grokHome = filepath.Join(home, ".grok")
	}
	if !filepath.IsAbs(grokHome) {
		absolute, err := filepath.Abs(grokHome)
		if err != nil {
			return "", err
		}
		grokHome = absolute
	}
	return filepath.Join(canonicalForCreate(grokHome), "installed-plugins"), nil
}

func canonicalForCreate(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	current := absolute
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absolute)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	if real, err := filepath.EvalSymlinks(current); err == nil {
		current = real
	}
	for index := len(suffix) - 1; index >= 0; index-- {
		current = filepath.Join(current, suffix[index])
	}
	return filepath.Clean(current)
}

func repoKey(source string) string {
	base := strings.TrimSuffix(strings.TrimRight(source, "/"), ".git")
	if index := strings.LastIndexAny(base, "/:"); index >= 0 {
		base = base[index+1:]
	}
	base = nameFromDir(base)
	if base == "" {
		base = "plugin"
	}
	digest := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%s-%x", base, digest[:4])
}

func pluginName(id string) string {
	id = strings.TrimSpace(id)
	if index := strings.LastIndexByte(id, '/'); index >= 0 {
		return id[index+1:]
	}
	return id
}

func sortedPluginNames(plugins map[string]RepoPlugin) []string {
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func pinnedRef(ref string) bool {
	return len(ref) == 40 && allHex(ref) || strings.HasPrefix(ref, "v") && strings.Contains(ref, ".")
}

func allHex(value string) bool {
	for _, char := range value {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F' {
			continue
		}
		return false
	}
	return true
}

func removeInstalledPath(path, installRoot string) error {
	path = filepath.Clean(path)
	if path == installRoot || !pathWithin(installRoot, path) {
		return fmt.Errorf("installed plugin path %q escapes %q", path, installRoot)
	}
	return os.RemoveAll(path)
}

func cleanupPluginData(repo InstalledRepo, grokHome string) {
	base := filepath.Join(grokHome, "plugin-data")
	for name, item := range repo.Plugins {
		root := repo.Path
		if item.Subdir != "" {
			root = filepath.Join(root, filepath.FromSlash(item.Subdir))
		}
		id := pluginID(userScope, canonicalOrClean(root), name)
		path := filepath.Join(base, filepath.FromSlash(id))
		if pathWithin(base, path) {
			_ = os.RemoveAll(path)
		}
	}
}
