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
	Description            string
	Path                   string
	Source                 string
	Paths                  []string
	WhenToUse              string
	DisableModelInvocation bool
	digest                 [sha256.Size]byte
}

type Config struct {
	Compat   compat.Config
	Paths    []string
	Ignore   []string
	Disabled []string
}

type skillRoot struct {
	path     string
	source   string
	commands bool
}

type Catalog struct {
	root     string
	compat   compat.Config
	byName   map[string]Skill
	pending  map[string]Skill
	checked  map[string]bool
	roots    []skillRoot
	ignore   []string
	disabled map[string]bool
	seen     map[string]bool
	changed  bool
	mu       sync.RWMutex
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
	var roots []skillRoot
	for _, path := range cfg.Paths {
		if path = resolveConfigPath(path, home, workspaceRoot); path != "" {
			roots = append(roots, skillRoot{path: path, source: "config"})
		}
	}
	if home != "" {
		if cfg.Compat.Cursor.Skills {
			roots = appendSkillRoots(roots, filepath.Join(home, ".cursor"), "user:cursor")
		}
		if cfg.Compat.Claude.Skills {
			roots = appendSkillRoots(roots, filepath.Join(home, ".claude"), "user:claude")
		}
		roots = appendSkillRoots(roots, filepath.Join(home, ".agents"), "user:agents")
	}
	if grokHome != "" {
		roots = appendSkillRoots(roots, grokHome, "user:grok")
	}
	gitRoot := workspace.GitRoot(workspaceRoot)
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		var dirs []string
		if cfg.Compat.Cursor.Skills {
			dirs = append(dirs, ".cursor")
		}
		if cfg.Compat.Claude.Skills {
			dirs = append(dirs, ".claude")
		}
		dirs = append(dirs, ".agents", ".gork", ".grok")
		for _, dir := range dirs {
			roots = appendSkillRoots(roots, filepath.Join(scope, dir), "workspace:"+strings.TrimPrefix(dir, "."))
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
		root: workspaceRoot, compat: cfg.Compat, roots: append([]skillRoot(nil), roots...), ignore: ignore, disabled: disabled,
		byName: make(map[string]Skill), pending: make(map[string]Skill), checked: make(map[string]bool), seen: make(map[string]bool),
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

func appendSkillRoots(roots []skillRoot, configDir, source string) []skillRoot {
	return append(roots,
		skillRoot{path: filepath.Join(configDir, "commands"), source: source, commands: true},
		skillRoot{path: filepath.Join(configDir, "skills"), source: source},
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
		err = c.scanCommands(root.path, root.source)
	} else {
		err = c.scan(root.path, root.source)
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

func (c *Catalog) scan(root, source string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat skills root %q: %w", root, err)
	}
	if !info.IsDir() {
		if !strings.EqualFold(filepath.Base(root), "SKILL.md") {
			return nil
		}
		return c.loadSkill(root, info, source, false)
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
		return c.loadSkill(path, info, source, false)
	})
}

func (c *Catalog) scanCommands(root, source string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read commands root %q: %w", root, err)
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
		if err := c.loadSkill(filepath.Join(root, entry.Name()), info, source, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) loadSkill(path string, info os.FileInfo, source string, command bool) error {
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
	if c.seen != nil {
		c.seen[metadata.Name] = true
	}
	if _, active := c.byName[metadata.Name]; !active {
		if _, held := c.pending[metadata.Name]; !held && len(c.byName)+len(c.pending) >= maxSkills {
			return errors.New("skill discovery exceeded 500 skills")
		}
	}
	// Later roots have higher priority, so workspace skills override config and user skills.
	active, wasActive := c.byName[metadata.Name]
	delete(c.byName, metadata.Name)
	delete(c.pending, metadata.Name)
	skill := Skill{
		Name: metadata.Name, Description: metadata.Description, Path: real, Source: source,
		Paths: metadata.Paths, WhenToUse: metadata.WhenToUse,
		DisableModelInvocation: metadata.DisableModelInvocation || c.disabled[metadata.Name], digest: sha256.Sum256(data),
	}
	if len(metadata.Paths) == 0 || wasActive && active.Path == real {
		c.byName[metadata.Name] = skill
	} else {
		if c.pending == nil {
			c.pending = make(map[string]Skill)
		}
		c.pending[metadata.Name] = skill
	}
	return nil
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
	fresh := &Catalog{
		root: c.root, compat: c.compat, roots: append([]skillRoot(nil), c.roots...),
		ignore: append([]string(nil), c.ignore...), disabled: c.disabled,
		byName: make(map[string]Skill, len(c.byName)), pending: make(map[string]Skill, len(c.pending)),
		checked: make(map[string]bool), seen: make(map[string]bool),
	}
	for name, skill := range c.byName {
		fresh.byName[name] = skill
	}
	for name, skill := range c.pending {
		fresh.pending[name] = skill
	}
	for _, root := range fresh.roots {
		if err := fresh.scanOnce(root); err != nil {
			return err
		}
	}
	for name := range fresh.byName {
		if !fresh.seen[name] {
			delete(fresh.byName, name)
		}
	}
	for name := range fresh.pending {
		if !fresh.seen[name] {
			delete(fresh.pending, name)
		}
	}
	changed := modelSkillsChanged(c.byName, fresh.byName)
	c.byName, c.pending, c.checked, c.seen = fresh.byName, fresh.pending, fresh.checked, fresh.seen
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
				_ = c.scanCommands(root.path, root.source)
			} else {
				_ = c.scan(root.path, root.source)
			}
		}
		return
	}
	for _, scope := range workspace.ProjectScopes(c.root, path) {
		for _, dir := range c.skillConfigDirs() {
			source := "workspace:" + strings.TrimPrefix(dir, ".")
			for _, root := range appendSkillRoots(nil, filepath.Join(scope, dir), source) {
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
				return skillRoot{
					path: root, source: "workspace:" + strings.TrimPrefix(configDir, "."), commands: kind == "commands",
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
	fmt.Fprintf(output, "- %s: %s\n", skill.Name, skill.Description)
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

func (c *Catalog) Tool() *Tool { return &Tool{catalog: c} }

type Tool struct{ catalog *Catalog }

func (t *Tool) Definition() api.ToolDefinition {
	t.catalog.mu.RLock()
	names := t.catalog.modelSkillNamesLocked()
	t.catalog.mu.RUnlock()
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
	t.catalog.mu.RLock()
	skill, ok := t.catalog.byName[args.Name]
	t.catalog.mu.RUnlock()
	if !ok || skill.DisableModelInvocation {
		return "", fmt.Errorf("unknown skill %q", args.Name)
	}
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", args.Name, err)
	}
	if len(data) > maxSkillBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("skill %q is too large or no longer UTF-8", args.Name)
	}
	return fmt.Sprintf(
		"<skill name=\"%s\" description=\"%s\" path=\"%s\">\n%s\n</skill>",
		skill.Name, skill.Description, skill.Path, data,
	), nil
}

type skillMetadata struct {
	Name                   string
	Description            string
	Paths                  []string
	WhenToUse              string
	DisableModelInvocation bool
}

func parseMetadata(content, fallbackName string) skillMetadata {
	fallbackName = normalizeSkillName(fallbackName)
	result := skillMetadata{Name: fallbackName}
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
		DisableModelInvocation yaml.Node `yaml:"disable-model-invocation"`
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
	result.DisableModelInvocation = metadata.DisableModelInvocation.Kind == yaml.ScalarNode && strings.EqualFold(metadata.DisableModelInvocation.Value, "true")
	result.Paths = parseSkillPaths(metadata.Paths)
	return result
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

func parseSkillPaths(node yaml.Node) []string {
	var raw []string
	switch node.Kind {
	case yaml.ScalarNode:
		raw = splitSkillPaths(node.Value)
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
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
	var paths []string
	start, depth := 0, 0
	for index, char := range value {
		switch char {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth <= 0 {
				paths = append(paths, strings.TrimSpace(value[start:index]))
				start = index + 1
			}
		}
	}
	return append(paths, strings.TrimSpace(value[start:]))
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
