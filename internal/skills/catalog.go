package skills

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
	"gopkg.in/yaml.v3"
)

const (
	maxSkillBytes            = 1 << 20
	maxSkillDescriptionChars = 1024
	maxSkills                = 500
)

var cursorDefaultSkills = map[string]bool{
	"babysit": true, "canvas": true, "create-hook": true, "create-rule": true,
	"create-skill": true, "create-subagent": true, "loop": true, "migrate-to-skills": true,
	"sdk": true, "shell": true, "split-to-prs": true, "statusline": true,
	"update-cli-config": true, "update-cursor-settings": true,
}

var claudeDefaultSkills = map[string]bool{
	"pdf": true, "docx": true, "xlsx": true, "pptx": true, "skill-creator": true,
}

type Skill struct {
	Name                   string
	DisplayName            string
	Description            string
	HasAuthoredDescription bool
	ShortDescription       string
	Author                 string
	ArgumentHint           string
	License                string
	Compatibility          string
	Metadata               map[string]string
	AllowedTools           []string
	Model                  string
	Effort                 string
	Path                   string
	Source                 string
	PluginName             string
	PluginRoot             string
	PluginData             string
	Paths                  []string
	WhenToUse              string
	UserInvocable          bool
	DisableModelInvocation bool
	baseName               string
	scope                  string
	rekeyed                bool
	frontmatterDisabled    bool
	digest                 [sha256.Size]byte
}

type Info struct {
	Name                        string            `json:"name"`
	DisplayName                 string            `json:"display_name,omitempty"`
	Description                 string            `json:"description"`
	HasUserSpecifiedDescription bool              `json:"has_user_specified_description"`
	Paths                       []string          `json:"paths,omitempty"`
	WhenToUse                   string            `json:"when_to_use,omitempty"`
	ShortDescription            string            `json:"short_description,omitempty"`
	Author                      string            `json:"author,omitempty"`
	ArgumentHint                string            `json:"argument_hint,omitempty"`
	License                     string            `json:"license,omitempty"`
	Compatibility               string            `json:"compatibility,omitempty"`
	Metadata                    map[string]string `json:"metadata,omitempty"`
	Path                        string            `json:"path"`
	Scope                       string            `json:"scope"`
	PluginName                  string            `json:"plugin_name,omitempty"`
	PluginRoot                  string            `json:"plugin_root,omitempty"`
	PluginData                  string            `json:"plugin_data,omitempty"`
	AllowedTools                []string          `json:"allowed_tools,omitempty"`
	Model                       string            `json:"model,omitempty"`
	Effort                      string            `json:"effort,omitempty"`
	UserInvocable               bool              `json:"user_invocable"`
	DisableModelInvocation      bool              `json:"disable_model_invocation"`
	Enabled                     bool              `json:"enabled"`
}

type ConfigInfo struct {
	Paths       []string `json:"paths"`
	Ignore      []string `json:"ignore"`
	TotalSkills int      `json:"totalSkills"`
	Message     string   `json:"message"`
	Skills      []Info   `json:"skills"`
}

type Config struct {
	Compat   compat.Config
	Paths    []string
	Ignore   []string
	Disabled []string
	Plugins  []plugin.Plugin
}

type Settings struct {
	Paths    []string
	Ignore   []string
	Disabled []string
}

type skillRoot struct {
	path       string
	source     string
	scope      string
	commands   bool
	pluginName string
	pluginRoot string
	pluginData string
}

type Catalog struct {
	root        string
	compat      compat.Config
	config      Config
	byName      map[string]Skill
	pending     map[string]Skill
	checked     map[string]bool
	roots       []skillRoot
	ignore      []string
	disabled    map[string]bool
	activePaths map[string]bool
	changed     bool
	mu          sync.RWMutex
}

func Discover(workspaceRoot string, cfg Config) (*Catalog, error) {
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	return discover(workspaceRoot, home, grokHome, cfg)
}

func discover(workspaceRoot, home, grokHome string, cfg Config) (*Catalog, error) {
	if real, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		workspaceRoot = real
	}
	gitRoot := workspace.GitRoot(workspaceRoot)
	_, gitErr := os.Stat(filepath.Join(gitRoot, ".git"))
	hasGitRoot := gitErr == nil
	var roots []skillRoot
	for index := len(cfg.Paths) - 1; index >= 0; index-- {
		path := cfg.Paths[index]
		if path = resolveConfigPath(path, home, workspaceRoot); path != "" {
			scope := "user"
			if hasGitRoot && pathWithin(gitRoot, path) {
				scope = "repo"
			}
			roots = append(roots, skillRoot{path: path, source: "config", scope: scope})
		}
	}
	if home != "" {
		if cfg.Compat.Cursor.Skills {
			roots = appendSkillRoots(roots, filepath.Join(home, ".cursor"), "user:cursor", "user")
		}
		if cfg.Compat.Claude.Skills {
			roots = appendSkillRoots(roots, filepath.Join(home, ".claude"), "user:claude", "user")
		}
		roots = appendSkillRoots(roots, filepath.Join(home, ".agents"), "user:agents", "user")
	}
	if grokHome != "" {
		roots = appendSkillRoots(roots, grokHome, "user:grok", "user")
	}
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		skillScope := "repo"
		if scope == workspaceRoot {
			skillScope = "local"
		}
		var dirs []string
		if cfg.Compat.Cursor.Skills {
			dirs = append(dirs, ".cursor")
		}
		if cfg.Compat.Claude.Skills {
			dirs = append(dirs, ".claude")
		}
		dirs = append(dirs, ".agents", ".gork", ".grok")
		for _, dir := range dirs {
			roots = appendSkillRoots(roots, filepath.Join(scope, dir), "workspace:"+strings.TrimPrefix(dir, "."), skillScope)
		}
	}
	for _, item := range cfg.Plugins {
		for _, path := range item.SkillDirs {
			roots = append(roots, skillRoot{
				path: path, source: "plugin:" + item.Name, scope: "plugin",
				pluginName: item.Name, pluginRoot: item.Root, pluginData: item.DataDir,
			})
		}
		for _, path := range item.CommandDirs {
			roots = append(roots, skillRoot{
				path: path, source: "plugin:" + item.Name, scope: "plugin", commands: true,
				pluginName: item.Name, pluginRoot: item.Root, pluginData: item.DataDir,
			})
		}
	}
	disabled := make(map[string]bool, len(cfg.Disabled))
	for _, name := range cfg.Disabled {
		disabled[name] = true
	}
	ignore := make([]string, 0, len(cfg.Ignore))
	for _, path := range cfg.Ignore {
		if path = resolveConfigPath(path, home, workspaceRoot); path != "" {
			ignore = append(ignore, path)
		}
	}
	catalog := &Catalog{
		root: workspaceRoot, compat: cfg.Compat, config: cloneConfig(cfg), roots: append([]skillRoot(nil), roots...), ignore: ignore, disabled: disabled,
		byName: make(map[string]Skill), pending: make(map[string]Skill), checked: make(map[string]bool),
	}
	for _, root := range roots {
		if root.path == "" {
			continue
		}
		if err := catalog.scanOnce(root); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func cloneConfig(cfg Config) Config {
	cfg.Paths = append([]string(nil), cfg.Paths...)
	cfg.Ignore = append([]string(nil), cfg.Ignore...)
	cfg.Disabled = append([]string(nil), cfg.Disabled...)
	cfg.Plugins = append([]plugin.Plugin(nil), cfg.Plugins...)
	return cfg
}

func (c *Catalog) Reconfigure(settings Settings) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	cfg := cloneConfig(c.config)
	c.mu.RUnlock()
	cfg.Paths = append([]string(nil), settings.Paths...)
	cfg.Ignore = append([]string(nil), settings.Ignore...)
	cfg.Disabled = append([]string(nil), settings.Disabled...)
	return c.rebuild(cfg)
}

func (c *Catalog) ReconfigurePlugins(plugins []plugin.Plugin) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	cfg := cloneConfig(c.config)
	c.mu.RUnlock()
	cfg.Plugins = append([]plugin.Plugin(nil), plugins...)
	return c.rebuild(cfg)
}

func (c *Catalog) Refresh() error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	cfg := cloneConfig(c.config)
	c.mu.RUnlock()
	return c.rebuild(cfg)
}

func (c *Catalog) rebuild(cfg Config) error {
	fresh, err := Discover(c.root, cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.compat, c.config = fresh.compat, fresh.config
	c.byName, c.pending, c.checked = fresh.byName, fresh.pending, fresh.checked
	c.roots, c.ignore, c.disabled = fresh.roots, fresh.ignore, fresh.disabled
	c.activePaths, c.changed = fresh.activePaths, true
	c.mu.Unlock()
	return nil
}

func ResolveConfigPath(path, workspaceRoot string) string {
	home, _ := os.UserHomeDir()
	return resolveConfigPath(path, home, workspaceRoot)
}

func appendSkillRoots(roots []skillRoot, configDir, source, scope string) []skillRoot {
	return append(roots,
		skillRoot{path: filepath.Join(configDir, "commands"), source: source, scope: scope, commands: true},
		skillRoot{path: filepath.Join(configDir, "skills"), source: source, scope: scope},
	)
}

func resolveConfigPath(path, home, workspaceRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" && home != "" {
		path = home
	} else if strings.HasPrefix(path, "~/") && home != "" {
		path = filepath.Join(home, path[2:])
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRoot, path)
	}
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func (c *Catalog) scanOnce(root skillRoot) error {
	root.path = filepath.Clean(root.path)
	key := root.path
	if root.commands {
		key = "commands:" + key
	}
	if c.checked[key] {
		return nil
	}
	var err error
	if root.commands {
		err = c.scanCommands(root)
	} else {
		err = c.scan(root)
	}
	if err != nil {
		return err
	}
	if c.checked == nil {
		c.checked = make(map[string]bool)
	}
	c.checked[key] = true
	return nil
}

func (c *Catalog) scan(root skillRoot) error {
	info, err := os.Stat(root.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat skills root %q: %w", root.path, err)
	}
	if !info.IsDir() {
		if !strings.EqualFold(filepath.Base(root.path), "SKILL.md") {
			return nil
		}
		return c.loadSkill(root.path, info, root, false)
	}
	var paths []string
	if err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		info, err := os.Stat(paths[index])
		if err != nil {
			return err
		}
		if err := c.loadSkill(paths[index], info, root, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) scanCommands(root skillRoot) error {
	entries, err := os.ReadDir(root.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read commands root %q: %w", root.path, err)
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := c.loadSkill(filepath.Join(root.path, entry.Name()), info, root, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) loadSkill(path string, info os.FileInfo, root skillRoot, command bool) error {
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
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve skill %q: %w", path, err)
	}
	for _, ignored := range c.ignore {
		if pathWithin(ignored, real) {
			return nil
		}
	}
	fallbackName := filepath.Base(filepath.Dir(path))
	if command {
		fallbackName = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	metadata := parseMetadata(string(data), fallbackName)
	if metadata.Name == "" || isVendorDefaultSkill(real, metadata.Name) {
		return nil
	}
	name := metadata.Name
	displayName := ""
	if root.pluginName != "" {
		name = normalizeSkillName(fallbackName)
		if name == "" {
			return nil
		}
		if metadata.Name != name {
			displayName = metadata.Name
		}
	}
	wasActive := c.removePath(real)
	skill := Skill{
		Name: name, DisplayName: displayName, Description: metadata.Description, Path: real, Source: root.source,
		PluginName: root.pluginName, PluginRoot: root.pluginRoot, PluginData: root.pluginData,
		Paths: metadata.Paths, WhenToUse: metadata.WhenToUse, UserInvocable: metadata.UserInvocable,
		HasAuthoredDescription: metadata.HasAuthoredDescription, ShortDescription: metadata.ShortDescription,
		Author: metadata.Author, ArgumentHint: metadata.ArgumentHint, License: metadata.License,
		Compatibility: metadata.Compatibility, Metadata: metadata.Metadata, AllowedTools: metadata.AllowedTools,
		Model: metadata.Model, Effort: metadata.Effort,
		baseName: normalizeSkillName(fallbackName), scope: root.scope,
		frontmatterDisabled: metadata.DisableModelInvocation, digest: sha256.Sum256(data),
	}
	active := len(metadata.Paths) == 0 || wasActive || c.activePaths[real]
	if skill.PluginName != "" {
		return c.storePluginSkill(skill, active)
	}
	return c.insertSkill(skill, active)
}

func (c *Catalog) storePluginSkill(skill Skill, active bool) error {
	name := qualifiedSkillName(skill)
	if _, _, exists := c.lookup(name); !exists {
		if err := c.requireCapacity(); err != nil {
			return err
		}
	}
	skill.DisableModelInvocation = skill.frontmatterDisabled || c.disabled[skill.Name] || c.disabled[name]
	if active {
		c.byName[name] = skill
	} else {
		c.pending[name] = skill
	}
	return nil
}

func (c *Catalog) insertSkill(skill Skill, active bool) error {
	incumbent, incumbentActive, exists := c.lookup(skill.Name)
	if !exists {
		return c.store(skill, active)
	}
	if incumbent.scope != skill.scope {
		c.deleteName(skill.Name)
		return c.store(skill, active)
	}
	incomingOwnsName := skill.baseName == skill.Name
	incumbentOwnsName := !incumbent.rekeyed && incumbent.baseName == incumbent.Name
	if incomingOwnsName && (incumbentOwnsName || incumbent.rekeyed) {
		c.deleteName(skill.Name)
		return c.store(skill, active)
	}
	if incomingOwnsName {
		if !c.canRekey(incumbent) {
			c.deleteName(incumbent.Name)
			return c.store(skill, active)
		}
		if err := c.requireCapacity(); err != nil {
			return err
		}
		c.deleteName(incumbent.Name)
		incumbent.DisplayName, incumbent.Name, incumbent.rekeyed = incumbent.Name, incumbent.baseName, true
		if err := c.store(incumbent, incumbentActive); err != nil {
			return err
		}
		return c.store(skill, active)
	}
	if incumbentOwnsName {
		if !c.canRekey(skill) {
			return nil
		}
		if err := c.requireCapacity(); err != nil {
			return err
		}
		skill.DisplayName, skill.Name, skill.rekeyed = skill.Name, skill.baseName, true
		return c.store(skill, active)
	}
	if c.canRekey(incumbent) {
		if err := c.requireCapacity(); err != nil {
			return err
		}
		c.deleteName(incumbent.Name)
		incumbent.DisplayName, incumbent.Name, incumbent.rekeyed = incumbent.Name, incumbent.baseName, true
		if err := c.store(incumbent, incumbentActive); err != nil {
			return err
		}
		return c.store(skill, active)
	}
	c.deleteName(skill.Name)
	return c.store(skill, active)
}

func (c *Catalog) canRekey(skill Skill) bool {
	if skill.baseName == "" || skill.baseName == skill.Name {
		return false
	}
	_, _, exists := c.lookup(skill.baseName)
	return !exists
}

func (c *Catalog) requireCapacity() error {
	if len(c.byName)+len(c.pending) >= maxSkills {
		return errors.New("skill discovery exceeded 500 skills")
	}
	return nil
}

func (c *Catalog) store(skill Skill, active bool) error {
	if _, _, exists := c.lookup(skill.Name); !exists {
		if err := c.requireCapacity(); err != nil {
			return err
		}
	}
	skill.DisableModelInvocation = skill.frontmatterDisabled || c.disabled[skill.Name] || c.disabled[qualifiedSkillName(skill)]
	if active {
		if c.byName == nil {
			c.byName = make(map[string]Skill)
		}
		c.byName[skill.Name] = skill
	} else {
		if c.pending == nil {
			c.pending = make(map[string]Skill)
		}
		c.pending[skill.Name] = skill
	}
	return nil
}

func (c *Catalog) lookup(name string) (Skill, bool, bool) {
	if skill, ok := c.byName[name]; ok {
		return skill, true, true
	}
	skill, ok := c.pending[name]
	return skill, false, ok
}

func (c *Catalog) deleteName(name string) {
	delete(c.byName, name)
	delete(c.pending, name)
}

func (c *Catalog) removePath(path string) bool {
	for name, skill := range c.byName {
		if skill.Path == path {
			delete(c.byName, name)
			return true
		}
	}
	for name, skill := range c.pending {
		if skill.Path == path {
			delete(c.pending, name)
			return false
		}
	}
	return false
}

func isVendorDefaultSkill(path, name string) bool {
	return pathHasDir(path, ".cursor") && cursorDefaultSkills[name] ||
		pathHasDir(path, ".claude") && claudeDefaultSkills[name]
}

func pathHasDir(path, name string) bool {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == name {
			return true
		}
		if parent := filepath.Dir(dir); parent == dir {
			return false
		}
	}
}

func (c *Catalog) Count() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byName) + len(c.pending)
}

func (c *Catalog) Clone() *Catalog {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := &Catalog{
		root: c.root, compat: c.compat, config: c.config,
		byName: make(map[string]Skill, len(c.byName)), pending: make(map[string]Skill, len(c.pending)),
		checked: make(map[string]bool, len(c.checked)), roots: append([]skillRoot(nil), c.roots...),
		ignore: append([]string(nil), c.ignore...), disabled: make(map[string]bool, len(c.disabled)),
		activePaths: make(map[string]bool, len(c.activePaths)),
	}
	result.config.Paths = append([]string(nil), c.config.Paths...)
	result.config.Ignore = append([]string(nil), c.config.Ignore...)
	result.config.Disabled = append([]string(nil), c.config.Disabled...)
	result.config.Plugins = append([]plugin.Plugin(nil), c.config.Plugins...)
	for name, skill := range c.byName {
		result.byName[name] = cloneSkill(skill)
	}
	for name, skill := range c.pending {
		result.pending[name] = cloneSkill(skill)
	}
	for name, value := range c.checked {
		result.checked[name] = value
	}
	for name, value := range c.disabled {
		result.disabled[name] = value
	}
	for path, value := range c.activePaths {
		result.activePaths[path] = value
	}
	return result
}

func cloneSkill(skill Skill) Skill {
	result := skill
	result.AllowedTools = append([]string(nil), skill.AllowedTools...)
	result.Paths = append([]string(nil), skill.Paths...)
	if skill.Metadata != nil {
		result.Metadata = make(map[string]string, len(skill.Metadata))
		for key, value := range skill.Metadata {
			result.Metadata[key] = value
		}
	}
	return result
}

func (c *Catalog) Preload(names []string) string {
	if c == nil || len(names) == 0 {
		return ""
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		c.mu.RLock()
		key, pending, skill, ok := c.resolveAnySkillLocked(name)
		c.mu.RUnlock()
		if !ok {
			continue
		}
		content, err := renderSkill(skill, "")
		if err != nil {
			continue
		}
		parts = append(parts, content)
		c.mu.Lock()
		if pending {
			delete(c.pending, key)
		} else {
			delete(c.byName, key)
		}
		c.mu.Unlock()
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(parts, "\n\n") + "\n\n"
}

func (c *Catalog) resolveAnySkillLocked(name string) (string, bool, Skill, bool) {
	for key, skill := range c.byName {
		if strings.EqualFold(key, name) || strings.EqualFold(qualifiedSkillName(skill), name) {
			return key, false, skill, true
		}
	}
	for key, skill := range c.pending {
		if strings.EqualFold(key, name) || strings.EqualFold(qualifiedSkillName(skill), name) {
			return key, true, skill, true
		}
	}
	return "", false, Skill{}, false
}

func (c *Catalog) List() []Info {
	if c == nil {
		return []Info{}
	}
	c.mu.RLock()
	result := make([]Info, 0, len(c.byName))
	for name, skill := range c.byName {
		result = append(result, skillInfo(skill, !c.disabled[name] && !c.disabled[qualifiedSkillName(skill)]))
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (c *Catalog) ConfigInfo() ConfigInfo {
	if c == nil {
		return ConfigInfo{Paths: []string{}, Ignore: []string{}, Skills: []Info{}, Message: "Total skills loaded: 0"}
	}
	c.mu.RLock()
	paths := make([]string, 0)
	seen := make(map[string]bool)
	for _, root := range c.roots {
		if root.source == "config" && !seen[root.path] {
			seen[root.path] = true
			paths = append(paths, root.path)
		}
	}
	ignore := append([]string(nil), c.ignore...)
	c.mu.RUnlock()
	sort.Strings(paths)
	sort.Strings(ignore)
	skills := c.List()
	return ConfigInfo{
		Paths: paths, Ignore: ignore, TotalSkills: len(skills), Skills: skills,
		Message: fmt.Sprintf("Total skills loaded: %d", len(skills)),
	}
}

func skillInfo(skill Skill, enabled bool) Info {
	return Info{
		Name: skill.Name, DisplayName: skill.DisplayName, Description: skill.Description,
		HasUserSpecifiedDescription: skill.HasAuthoredDescription, Paths: append([]string(nil), skill.Paths...),
		WhenToUse: skill.WhenToUse, ShortDescription: skill.ShortDescription, Author: skill.Author,
		ArgumentHint: skill.ArgumentHint, License: skill.License, Compatibility: skill.Compatibility,
		Metadata: skill.Metadata, Path: skill.Path, Scope: skill.scope, PluginName: skill.PluginName,
		PluginRoot: skill.PluginRoot, PluginData: skill.PluginData, AllowedTools: append([]string(nil), skill.AllowedTools...),
		Model: skill.Model, Effort: skill.Effort, UserInvocable: skill.UserInvocable,
		DisableModelInvocation: skill.DisableModelInvocation, Enabled: enabled,
	}
}

func (c *Catalog) Watch(ctx context.Context, interval time.Duration) {
	if c == nil {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.reload()
			}
		}
	}()
}

func (c *Catalog) reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	activePaths := make(map[string]bool, len(c.byName))
	for _, skill := range c.byName {
		activePaths[skill.Path] = true
	}
	fresh := &Catalog{
		root: c.root, compat: c.compat, roots: append([]skillRoot(nil), c.roots...),
		ignore: append([]string(nil), c.ignore...), disabled: c.disabled,
		byName: make(map[string]Skill, len(c.byName)), pending: make(map[string]Skill, len(c.pending)),
		checked: make(map[string]bool), activePaths: activePaths,
	}
	for _, root := range fresh.roots {
		if err := fresh.scanOnce(root); err != nil {
			return err
		}
	}
	changed := modelSkillsChanged(c.byName, fresh.byName)
	c.byName, c.pending, c.checked = fresh.byName, fresh.pending, fresh.checked
	if changed {
		c.changed = true
	}
	return nil
}

func modelSkillsChanged(before, after map[string]Skill) bool {
	visibleAfter := 0
	for name, skill := range after {
		if skill.DisableModelInvocation {
			continue
		}
		visibleAfter++
		previous, existed := before[name]
		if !existed || previous.DisableModelInvocation || previous.Path != skill.Path || previous.Source != skill.Source || previous.Description != skill.Description || previous.digest != skill.digest || strings.Join(previous.Paths, "\x00") != strings.Join(skill.Paths, "\x00") {
			return true
		}
	}
	visibleBefore := 0
	for _, skill := range before {
		if !skill.DisableModelInvocation {
			visibleBefore++
		}
	}
	return visibleBefore != visibleAfter
}

func (c *Catalog) DrainReminder() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.changed {
		return ""
	}
	names := c.modelSkillNamesLocked()
	c.changed = false
	var output strings.Builder
	output.WriteString("<system-reminder>\nSkills changed on disk:\n")
	if len(names) == 0 {
		output.WriteString("- No skills are currently available.\n")
	} else {
		for _, name := range names {
			writeSkillListing(&output, c.byName[name])
		}
	}
	output.WriteString("Use the skill tool to load one when it matches the task.\n</system-reminder>")
	return output.String()
}

// Activate updates skill visibility after a successful file tool call and
// returns a synthetic reminder for the next model step.
func (c *Catalog) Activate(toolName string, raw json.RawMessage) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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
	before := make(map[string]string, len(c.byName))
	for name, skill := range c.byName {
		before[name] = skill.Path
	}
	for name, skill := range c.pending {
		if matchesPaths(skill.Paths, rel) {
			c.byName[name] = skill
			delete(c.pending, name)
		}
	}
	c.discoverForPath(path)
	var activated []string
	for name, skill := range c.byName {
		if before[name] != skill.Path && !skill.DisableModelInvocation {
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
		writeSkillListing(&output, c.byName[name])
	}
	output.WriteString("Use the skill tool to load one when it matches the task.\n</system-reminder>")
	return output.String()
}

func (c *Catalog) discoverForPath(path string) {
	if filepath.Ext(path) == ".md" {
		if root, ok := c.skillRootForPath(path); ok {
			c.addRoot(root)
			if root.commands {
				_ = c.scanCommands(root)
			} else {
				_ = c.scan(root)
			}
		}
		return
	}
	for _, scope := range workspace.ProjectScopes(c.root, path) {
		skillScope := "repo"
		if scope == c.root {
			skillScope = "local"
		}
		for _, dir := range c.skillConfigDirs() {
			source := "workspace:" + strings.TrimPrefix(dir, ".")
			for _, root := range appendSkillRoots(nil, filepath.Join(scope, dir), source, skillScope) {
				c.addRoot(root)
				_ = c.scanOnce(root)
			}
		}
	}
}

func (c *Catalog) addRoot(candidate skillRoot) {
	candidate.path = filepath.Clean(candidate.path)
	for _, root := range c.roots {
		if root.commands == candidate.commands && (root.path == candidate.path || pathWithin(root.path, candidate.path)) {
			return
		}
	}
	c.roots = append(c.roots, candidate)
}

func (c *Catalog) skillConfigDirs() []string {
	var dirs []string
	if c.compat.Cursor.Skills {
		dirs = append(dirs, ".cursor")
	}
	if c.compat.Claude.Skills {
		dirs = append(dirs, ".claude")
	}
	return append(dirs, ".agents", ".gork", ".grok")
}

func (c *Catalog) skillRootForPath(path string) (skillRoot, bool) {
	for dir := filepath.Dir(path); dir != c.root && pathWithin(c.root, dir); dir = filepath.Dir(dir) {
		kind := filepath.Base(dir)
		if kind != "skills" && kind != "commands" {
			continue
		}
		configDir := filepath.Base(filepath.Dir(dir))
		for _, allowed := range c.skillConfigDirs() {
			if configDir == allowed {
				root := dir
				if kind == "skills" {
					root = filepath.Dir(path)
				}
				scope := "repo"
				if filepath.Dir(filepath.Dir(dir)) == c.root {
					scope = "local"
				}
				return skillRoot{
					path: root, source: "workspace:" + strings.TrimPrefix(configDir, "."), scope: scope, commands: kind == "commands",
				}, true
			}
		}
	}
	return skillRoot{}, false
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := c.modelSkillNamesLocked()
	if len(names) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("The following skills are available for use:\n\n")
	for _, name := range names {
		writeSkillListing(&output, c.byName[name])
	}
	return output.String()
}

func writeSkillListing(output *strings.Builder, skill Skill) {
	name := skill.Name
	if skill.PluginName != "" {
		name = qualifiedSkillName(skill)
	}
	fmt.Fprintf(output, "- %s: %s\n", name, skill.Description)
	if skill.WhenToUse != "" {
		fmt.Fprintf(output, "  Use when: %s\n", skill.WhenToUse)
	}
	fmt.Fprintf(output, "  Absolute path: %s\n", skill.Path)
}

func (c *Catalog) Names() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.namesLocked()
}

func (c *Catalog) namesLocked() []string {
	names := make([]string, 0, len(c.byName))
	for name := range c.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) modelSkillNamesLocked() []string {
	names := make([]string, 0, len(c.byName))
	for name, skill := range c.byName {
		if !skill.DisableModelInvocation {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) toolSkillNamesLocked() []string {
	names := make([]string, 0, len(c.byName)*2)
	for name, skill := range c.byName {
		if skill.UserInvocable && !skill.DisableModelInvocation {
			names = append(names, name)
			if qualified := qualifiedSkillName(skill); qualified != name {
				names = append(names, qualified)
			}
		}
	}
	for name := range c.pluginBareAliasesLocked(true) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) pluginBareAliasesLocked(modelInvocable bool) map[string]Skill {
	aliases := make(map[string]Skill)
	ambiguous := make(map[string]bool)
	for key, skill := range c.byName {
		if skill.PluginName == "" || !skill.UserInvocable || modelInvocable && skill.DisableModelInvocation {
			continue
		}
		name := strings.ToLower(skill.Name)
		if _, native := c.byName[name]; native && key != name {
			ambiguous[name] = true
			delete(aliases, name)
			continue
		}
		if _, exists := aliases[name]; exists {
			ambiguous[name] = true
			delete(aliases, name)
		} else if !ambiguous[name] {
			aliases[name] = skill
		}
	}
	return aliases
}

func (c *Catalog) Tool() *Tool { return &Tool{catalog: c} }

type Tool struct{ catalog *Catalog }

func (t *Tool) Definition() api.ToolDefinition {
	t.catalog.mu.RLock()
	names := t.catalog.toolSkillNamesLocked()
	t.catalog.mu.RUnlock()
	nameSchema := map[string]any{"type": "string"}
	if len(names) > 0 {
		nameSchema["enum"] = names
	}
	return api.ToolDefinition{
		Type: "function", Name: "skill",
		Description: "Load the complete SKILL.md instructions for one discovered skill. Available names: " + strings.Join(names, ", "),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": nameSchema,
				"args": map[string]any{"type": "string", "description": "Optional arguments for the skill"},
			},
			"required": []string{"name"}, "additionalProperties": false,
		},
	}
}

func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode skill arguments: %w", err)
	}
	t.catalog.mu.RLock()
	skill, ok := t.catalog.resolveSkillLocked(args.Name)
	t.catalog.mu.RUnlock()
	if !ok || !skill.UserInvocable || skill.DisableModelInvocation {
		return "", fmt.Errorf("unknown skill %q", args.Name)
	}
	return renderSkill(skill, args.Args)
}

func renderSkill(skill Skill, args string) (string, error) {
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", skill.Name, err)
	}
	if len(data) > maxSkillBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("skill %q is too large or no longer UTF-8", skill.Name)
	}
	body := substituteSkillArguments(string(data), args, expansionContext{
		SkillDir: filepath.Dir(skill.Path), PluginRoot: skill.PluginRoot, PluginData: skill.PluginData,
	})
	return fmt.Sprintf(
		"<skill name=\"%s\" description=\"%s\" path=\"%s\">\n%s\n</skill>",
		skill.Name, skill.Description, skill.Path, body,
	), nil
}

type skillMetadata struct {
	Name                   string
	Description            string
	HasAuthoredDescription bool
	ShortDescription       string
	Author                 string
	ArgumentHint           string
	License                string
	Compatibility          string
	Metadata               map[string]string
	AllowedTools           []string
	Model                  string
	Effort                 string
	Paths                  []string
	WhenToUse              string
	UserInvocable          bool
	DisableModelInvocation bool
}

func parseMetadata(content, fallbackName string) skillMetadata {
	fallbackName = normalizeSkillName(fallbackName)
	result := skillMetadata{Name: fallbackName, UserInvocable: true}
	body := content
	if !strings.HasPrefix(content, "---\n") {
		result.Description = capSkillText(descriptionFromBody(body, result.Name))
		return result
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		result.Description = capSkillText(descriptionFromBody(body, result.Name))
		return result
	}
	body = content[4+end+5:]
	var metadata struct {
		Name                   yaml.Node `yaml:"name"`
		Description            yaml.Node `yaml:"description"`
		Paths                  yaml.Node `yaml:"paths"`
		WhenToUse              yaml.Node `yaml:"when-to-use"`
		WhenToUseAlias         yaml.Node `yaml:"when_to_use"`
		UserInvocable          yaml.Node `yaml:"user-invocable"`
		DisableModelInvocation yaml.Node `yaml:"disable-model-invocation"`
		ArgumentHint           yaml.Node `yaml:"argument-hint"`
		License                yaml.Node `yaml:"license"`
		Compatibility          yaml.Node `yaml:"compatibility"`
		AllowedTools           yaml.Node `yaml:"allowed-tools"`
		Model                  yaml.Node `yaml:"model"`
		Effort                 yaml.Node `yaml:"effort"`
		Metadata               yaml.Node `yaml:"metadata"`
	}
	if yaml.Unmarshal([]byte(content[4:4+end]), &metadata) != nil {
		result.Description = capSkillText(descriptionFromBody(body, result.Name))
		return result
	}
	if metadata.Name.Kind == yaml.ScalarNode {
		if candidate := normalizeSkillName(metadata.Name.Value); candidate != "" {
			result.Name = candidate
		}
	}
	if metadata.Description.Kind == yaml.ScalarNode {
		result.Description = capSkillText(metadata.Description.Value)
		result.HasAuthoredDescription = strings.TrimSpace(metadata.Description.Value) != ""
	}
	if result.Description == "" {
		result.Description = capSkillText(descriptionFromBody(body, result.Name))
	}
	whenToUse := metadata.WhenToUse
	if whenToUse.Kind == 0 {
		whenToUse = metadata.WhenToUseAlias
	}
	if whenToUse.Kind == yaml.ScalarNode {
		result.WhenToUse = capSkillText(whenToUse.Value)
	}
	if metadata.UserInvocable.Kind != 0 {
		result.UserInvocable = metadata.UserInvocable.Kind == yaml.ScalarNode && metadata.UserInvocable.Value == "true"
	}
	result.DisableModelInvocation = metadata.DisableModelInvocation.Kind == yaml.ScalarNode && metadata.DisableModelInvocation.Value == "true"
	result.Paths = parseSkillPaths(metadata.Paths)
	result.ArgumentHint = skillScalar(metadata.ArgumentHint)
	result.License = skillScalar(metadata.License)
	result.Compatibility = skillScalar(metadata.Compatibility)
	result.Model = skillScalar(metadata.Model)
	result.Effort = skillScalar(metadata.Effort)
	result.AllowedTools = parseAllowedTools(metadata.AllowedTools)
	result.ShortDescription, result.Author, result.Metadata = parseSkillMetadata(metadata.Metadata)
	return result
}

func (c *Catalog) resolveSkillLocked(name string) (Skill, bool) {
	if skill, ok := c.byName[name]; ok {
		return skill, true
	}
	for skillName, skill := range c.byName {
		if strings.EqualFold(skillName, name) || strings.EqualFold(qualifiedSkillName(skill), name) {
			return skill, true
		}
	}
	if skill, ok := c.pluginBareAliasesLocked(true)[strings.ToLower(name)]; ok {
		return skill, true
	}
	return Skill{}, false
}

func qualifiedSkillName(skill Skill) string {
	if skill.PluginName != "" {
		return skill.PluginName + ":" + skill.Name
	}
	if skill.scope == "" {
		return skill.Name
	}
	return skill.scope + ":" + skill.Name
}

func capSkillText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxSkillDescriptionChars {
		runes = runes[:maxSkillDescriptionChars]
	}
	return string(runes)
}

func normalizeSkillName(name string) string {
	var slug strings.Builder
	hyphen := false
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			slug.WriteRune(char)
			hyphen = false
		} else if slug.Len() > 0 && !hyphen {
			slug.WriteByte('-')
			hyphen = true
		}
	}
	return strings.Trim(slug.String(), "-")
}

func skillScalar(node yaml.Node) string {
	if node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func parseSkillMetadata(node yaml.Node) (shortDescription, author string, metadata map[string]string) {
	if node.Kind != yaml.MappingNode {
		return "", "", nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			continue
		}
		name, text := strings.TrimSpace(key.Value), strings.TrimSpace(value.Value)
		if name == "" || text == "" {
			continue
		}
		switch name {
		case "short-description":
			shortDescription = text
		case "author":
			author = text
		default:
			if metadata == nil {
				metadata = make(map[string]string)
			}
			metadata[name] = text
		}
	}
	return shortDescription, author, metadata
}

func parseAllowedTools(node yaml.Node) []string {
	var tools []string
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!str" {
			tools = splitTopLevel(node.Value, '(', ')', true)
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode && item.Tag == "!!str" && strings.TrimSpace(item.Value) != "" {
				tools = append(tools, strings.TrimSpace(item.Value))
			}
		}
	}
	return tools
}

func splitTopLevel(value string, open, close rune, splitSpace bool) []string {
	var values []string
	var current strings.Builder
	depth := 0
	flush := func() {
		if value := strings.TrimSpace(current.String()); value != "" {
			values = append(values, value)
		}
		current.Reset()
	}
	for _, char := range value {
		switch {
		case char == open:
			depth++
			current.WriteRune(char)
		case char == close:
			depth--
			current.WriteRune(char)
		case depth <= 0 && (char == ',' || splitSpace && (char == ' ' || char == '\t' || char == '\n')):
			flush()
		default:
			current.WriteRune(char)
		}
	}
	flush()
	return values
}

func parseSkillPaths(node yaml.Node) []string {
	var raw []string
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!str" {
			raw = splitSkillPaths(node.Value)
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode && item.Tag == "!!str" {
				raw = append(raw, splitSkillPaths(item.Value)...)
			}
		}
	}
	paths := raw[:0]
	for _, path := range raw {
		path = strings.TrimSpace(path)
		path = strings.TrimSuffix(path, "/**")
		if path != "" {
			paths = append(paths, path)
		}
	}
	for _, path := range paths {
		if path != "**" {
			return paths
		}
	}
	return nil
}

func splitSkillPaths(value string) []string {
	return splitTopLevel(value, '{', '}', false)
}

func descriptionFromBody(body, fallback string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var heading string
	for index := 0; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			index++
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			for index++; index < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[index]), marker); index++ {
			}
			index++
			continue
		}
		if strings.HasPrefix(line, "#") {
			if heading == "" {
				heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
			index++
			continue
		}
		if structuralMarkdown(line) {
			index++
			continue
		}
		var paragraph []string
		for ; index < len(lines); index++ {
			line = strings.TrimSpace(lines[index])
			if line == "" || strings.HasPrefix(line, "#") || structuralMarkdown(line) {
				break
			}
			paragraph = append(paragraph, line)
		}
		if len(paragraph) > 0 {
			return strings.Join(strings.Fields(strings.Join(paragraph, " ")), " ")
		}
	}
	if heading != "" {
		return heading
	}
	return fallback
}

func structuralMarkdown(line string) bool {
	return strings.HasPrefix(line, "![") || strings.HasPrefix(line, ">") ||
		strings.HasPrefix(line, "|") || strings.HasPrefix(line, "-") ||
		strings.HasPrefix(line, "*") || strings.HasPrefix(line, "+") ||
		len(line) > 2 && line[0] >= '0' && line[0] <= '9' && (line[1] == '.' || line[1] == ')')
}
