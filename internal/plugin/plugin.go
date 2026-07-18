package plugin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type Config struct {
	Paths          []string
	Enabled        []string
	Disabled       []string
	ProjectTrusted bool
}

type Plugin struct {
	ID          string
	Name        string
	Scope       string
	Version     string
	Description string
	Root        string
	DataDir     string
	SkillDirs   []string
	CommandDirs []string
	MCPConfig   string
	InlineMCP   json.RawMessage
	LSPConfig   string
	InlineLSP   json.RawMessage
	Enabled     bool
	Trusted     bool
	Executable  bool
}

type scope string

const (
	projectScope scope = "project"
	userScope    scope = "user"
	configScope  scope = "config"
)

type manifest struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description"`
	Skills      pathList  `json:"skills"`
	Commands    pathList  `json:"commands"`
	MCPServers  component `json:"mcpServers"`
	LSPServers  component `json:"lspServers"`
}

type component struct {
	Path   string
	Inline json.RawMessage
}

func (c *component) UnmarshalJSON(data []byte) error {
	var path string
	if json.Unmarshal(data, &path) == nil {
		c.Path = path
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("plugin component must be a path or object: %w", err)
	}
	c.Inline = append(c.Inline[:0], data...)
	return nil
}

type pathList []string

func (p *pathList) UnmarshalJSON(data []byte) error {
	var single string
	if json.Unmarshal(data, &single) == nil {
		*p = []string{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return fmt.Errorf("plugin component path must be a string or list of strings: %w", err)
	}
	*p = multiple
	return nil
}

func Discover(workspaceRoot string, cfg Config) ([]Plugin, error) {
	plugins, err := Inventory(workspaceRoot, cfg)
	if err != nil {
		return nil, err
	}
	enabled := make([]Plugin, 0, len(plugins))
	for _, item := range plugins {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	return enabled, nil
}

func Inventory(workspaceRoot string, cfg Config) ([]Plugin, error) {
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	return discoverInventory(workspaceRoot, home, grokHome, cfg)
}

func discover(workspaceRoot, home, grokHome string, cfg Config) ([]Plugin, error) {
	plugins, err := discoverInventory(workspaceRoot, home, grokHome, cfg)
	if err != nil {
		return nil, err
	}
	enabled := make([]Plugin, 0, len(plugins))
	for _, item := range plugins {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	return enabled, nil
}

func discoverInventory(workspaceRoot, home, grokHome string, cfg Config) ([]Plugin, error) {
	workspaceRoot = canonicalOrClean(workspaceRoot)
	gitRoot := workspace.GitRoot(workspaceRoot)
	seenPaths := make(map[string]bool)
	seenNames := make(map[string]bool)
	var plugins []Plugin

	collectParent := func(parent string, kind scope) error {
		entries, err := os.ReadDir(parent)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read plugins directory %q: %w", parent, err)
		}
		var roots []string
		for _, entry := range entries {
			root := filepath.Join(parent, entry.Name())
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				roots = append(roots, root)
			}
		}
		sort.Strings(roots)
		for _, root := range roots {
			collect(root, kind, kind != projectScope || cfg.ProjectTrusted, grokHome, cfg, seenPaths, seenNames, &plugins)
		}
		return nil
	}

	for _, dir := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		for _, relative := range []string{filepath.Join(".grok", "plugins"), filepath.Join(".claude", "plugins")} {
			if err := collectParent(filepath.Join(dir, relative), projectScope); err != nil {
				return nil, err
			}
		}
	}
	if grokHome != "" {
		if err := collectParent(filepath.Join(grokHome, "plugins"), userScope); err != nil {
			return nil, err
		}
	}
	if home != "" {
		if err := collectParent(filepath.Join(home, ".claude", "plugins"), userScope); err != nil {
			return nil, err
		}
	}
	for _, raw := range cfg.Paths {
		root := resolvePath(raw, home, workspaceRoot)
		if root != "" {
			collect(root, configScope, true, grokHome, cfg, seenPaths, seenNames, &plugins)
		}
	}
	return plugins, nil
}

func collect(root string, kind scope, executable bool, grokHome string, cfg Config, seenPaths, seenNames map[string]bool, plugins *[]Plugin) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil || seenPaths[root] {
		return
	}
	seenPaths[root] = true
	m, ok := loadManifest(root)
	if !ok || seenNames[m.Name] {
		return
	}
	id := pluginID(kind, root, m.Name)
	seenNames[m.Name] = true
	enabled := isEnabled(kind, m.Name, id, cfg)
	dataDir := ""
	if grokHome != "" {
		dataDir = filepath.Join(grokHome, "plugin-data", filepath.FromSlash(id))
	}
	*plugins = append(*plugins, Plugin{
		ID: id, Name: m.Name, Scope: string(kind), Version: m.Version, Description: m.Description,
		Root: root, DataDir: dataDir, Enabled: enabled, Trusted: executable, Executable: enabled && executable,
		SkillDirs: resolveDirs(root, m.Skills, "skills"), CommandDirs: resolveDirs(root, m.Commands, "commands"),
		MCPConfig: resolveMCPConfig(root, m.MCPServers), InlineMCP: append(json.RawMessage(nil), m.MCPServers.Inline...),
		LSPConfig: resolveLSPConfig(root, m.LSPServers), InlineLSP: append(json.RawMessage(nil), m.LSPServers.Inline...),
	})
}

func loadManifest(root string) (manifest, bool) {
	for _, relative := range []string{"plugin.json", filepath.Join(".grok-plugin", "plugin.json"), filepath.Join(".claude-plugin", "plugin.json")} {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return manifest{}, false
		}
		var m manifest
		if json.Unmarshal(data, &m) != nil || !validName(m.Name) {
			return manifest{}, false
		}
		return m, true
	}
	name := nameFromDir(root)
	if name == "" || !isDir(filepath.Join(root, "skills")) && !isDir(filepath.Join(root, "commands")) && !isFile(filepath.Join(root, ".mcp.json")) && !isFile(filepath.Join(root, ".lsp.json")) {
		return manifest{}, false
	}
	return manifest{Name: name}, true
}

func resolveLSPConfig(root string, configured component) string {
	if len(configured.Inline) > 0 {
		return ""
	}
	if configured.Path != "" {
		return resolveFile(root, configured.Path)
	}
	return resolveFile(root, ".lsp.json")
}

func resolveMCPConfig(root string, configured component) string {
	if configured.Path != "" {
		return resolveFile(root, configured.Path)
	}
	return resolveFile(root, ".mcp.json")
}

func resolveFile(root, relative string) string {
	if filepath.IsAbs(relative) {
		return ""
	}
	path := filepath.Join(root, relative)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithin(root, real) {
		return ""
	}
	return real
}

func resolveDirs(root string, configured pathList, fallback string) []string {
	if configured == nil {
		configured = pathList{fallback}
	}
	var dirs []string
	for _, relative := range configured {
		if filepath.IsAbs(relative) {
			continue
		}
		candidate := filepath.Join(root, relative)
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		real, err := filepath.EvalSymlinks(candidate)
		if err == nil && pathWithin(root, real) {
			dirs = append(dirs, real)
		}
	}
	return dirs
}

func isEnabled(kind scope, name, id string, cfg Config) bool {
	if listed(cfg.Disabled, name, id) {
		return false
	}
	return kind == configScope || listed(cfg.Enabled, name, id)
}

func listed(values []string, name, id string) bool {
	for _, value := range values {
		if value == name || value == id {
			return true
		}
	}
	return false
}

func pluginID(kind scope, root, name string) string {
	digest := sha256.Sum256([]byte(root))
	return fmt.Sprintf("%s/%x/%s", kind, digest[:4], name)
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, char := range name {
		if char < 'a' || char > 'z' {
			if char < '0' || char > '9' {
				if char != '-' {
					return false
				}
			}
		}
	}
	return true
}

func nameFromDir(root string) string {
	var name strings.Builder
	hyphen := false
	for _, char := range strings.ToLower(filepath.Base(root)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			name.WriteRune(char)
			hyphen = char == '-'
		} else if name.Len() > 0 && !hyphen {
			name.WriteByte('-')
			hyphen = true
		}
	}
	value := strings.Trim(name.String(), "-")
	if !validName(value) {
		return ""
	}
	return value
}

func resolvePath(path, home, workspaceRoot string) string {
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
	return canonicalOrClean(path)
}

func canonicalOrClean(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
