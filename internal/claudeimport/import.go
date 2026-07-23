package claudeimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

type Scope string
type Kind string

const (
	Global  Scope = "global"
	Project Scope = "project"

	Permission  Kind = "permission"
	Environment Kind = "environment"
	MCP         Kind = "mcp"
	Hook        Kind = "hook"
	Skill       Kind = "skill"
	Rule        Kind = "rule"
)

type Item struct {
	ID         string
	Scope      Scope
	Kind       Kind
	Name       string
	Source     string
	Permission config.PermissionRule
	Value      string
	MCP        config.MCPServerConfig
	Hook       hookItem
}

type hookItem struct {
	Event, Matcher, Command string
	Timeout                 *uint64
}

type Plan struct {
	Items       []Item
	Warnings    []string
	Home        string
	ProjectRoot string
}

type Result struct {
	Imported      int
	ModifiedFiles []string
}

func (i Item) Label() string {
	switch i.Kind {
	case Environment:
		return i.Name + " = <redacted>"
	case Permission:
		label := i.Permission.Action + " " + i.Permission.Tool
		if i.Permission.Pattern != nil {
			label += "(" + *i.Permission.Pattern + ")"
		}
		return label
	case Hook:
		return i.Hook.Event + ": " + i.Hook.Command
	default:
		return i.Name
	}
}

func Scan(cwd string) Plan {
	home, _ := os.UserHomeDir()
	return scan(cwd, home)
}

func scan(cwd, home string) Plan {
	root := workspace.GitRoot(cwd)
	plan := Plan{Home: home, ProjectRoot: root}
	if home != "" {
		for _, source := range []struct {
			scope Scope
			path  string
		}{
			{Global, filepath.Join(home, ".claude", "settings.json")},
			{Global, filepath.Join(home, ".claude", "settings.local.json")},
		} {
			plan.scanSettings(source.path, source.scope)
		}
	}
	for _, scope := range workspace.ProjectScopes(root, cwd) {
		plan.scanSettings(filepath.Join(scope, ".claude", "settings.json"), Project)
		plan.scanSettings(filepath.Join(scope, ".claude", "settings.local.json"), Project)
	}
	if home != "" {
		plan.scanClaudeJSON(filepath.Join(home, ".claude.json"), cwd)
	}
	for _, scope := range workspace.ProjectScopes(root, cwd) {
		plan.scanMCPFile(filepath.Join(scope, ".mcp.json"), Project)
	}
	if home != "" {
		plan.scanDir(filepath.Join(home, ".claude", "skills"), Global, Skill)
		plan.scanDir(filepath.Join(home, ".claude", "rules"), Global, Rule)
	}
	if home == "" || !samePath(home, root) {
		plan.scanDir(filepath.Join(root, ".claude", "skills"), Project, Skill)
		plan.scanDir(filepath.Join(root, ".claude", "rules"), Project, Rule)
	}
	plan.deduplicateOverrides()
	for index := range plan.Items {
		plan.Items[index].ID = fmt.Sprintf("%s:%s:%d", plan.Items[index].Scope, plan.Items[index].Kind, index)
	}
	return plan
}

type settingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Ask   []string `json:"ask"`
	} `json:"permissions"`
	Env   map[string]string            `json:"env"`
	Hooks map[string][]json.RawMessage `json:"hooks"`
}

func (p *Plan) scanSettings(path string, scope Scope) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("read %s: %v", path, err))
		return
	}
	var settings settingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("parse %s: %v", path, err))
		return
	}
	for _, group := range []struct {
		action string
		rules  []string
	}{{"allow", settings.Permissions.Allow}, {"deny", settings.Permissions.Deny}, {"ask", settings.Permissions.Ask}} {
		for _, raw := range group.rules {
			rule, ok := parsePermission(raw, group.action)
			if !ok {
				p.Warnings = append(p.Warnings, fmt.Sprintf("unsupported permission %q in %s", raw, path))
				continue
			}
			p.Items = append(p.Items, Item{Scope: scope, Kind: Permission, Name: raw, Source: path, Permission: rule})
		}
	}
	keys := make([]string, 0, len(settings.Env))
	for key := range settings.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.Items = append(p.Items, Item{Scope: scope, Kind: Environment, Name: key, Value: settings.Env[key], Source: path})
	}
	for event, groups := range settings.Hooks {
		for _, raw := range groups {
			var group struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string  `json:"type"`
					Command string  `json:"command"`
					Timeout *uint64 `json:"timeout"`
				} `json:"hooks"`
			}
			if json.Unmarshal(raw, &group) != nil {
				continue
			}
			for _, handler := range group.Hooks {
				if handler.Type != "command" || strings.TrimSpace(handler.Command) == "" {
					continue
				}
				hook := hookItem{Event: event, Matcher: group.Matcher, Command: handler.Command, Timeout: handler.Timeout}
				p.Items = append(p.Items, Item{Scope: scope, Kind: Hook, Name: event, Source: path, Hook: hook})
			}
		}
	}
}

func parsePermission(raw, action string) (config.PermissionRule, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "*" {
		return config.PermissionRule{Action: action, Tool: "any"}, true
	}
	tool, pattern := raw, ""
	if before, after, ok := strings.Cut(raw, "("); ok && strings.HasSuffix(after, ")") {
		tool, pattern = before, strings.TrimSuffix(after, ")")
	}
	mapped := map[string]string{"Bash": "bash", "Edit": "edit", "Write": "edit", "Read": "read", "Grep": "grep", "Glob": "grep", "MCPTool": "mcp", "WebFetch": "webfetch"}[tool]
	if mapped == "" {
		return config.PermissionRule{}, false
	}
	rule := config.PermissionRule{Action: action, Tool: mapped}
	if pattern != "" {
		if mapped == "webfetch" && strings.HasPrefix(pattern, "domain:") {
			pattern, rule.PatternMode = strings.TrimPrefix(pattern, "domain:"), "domain"
		} else {
			rule.PatternMode = "glob"
		}
		rule.Pattern = &pattern
	}
	return rule, true
}

func (p *Plan) scanClaudeJSON(path, cwd string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		p.Warnings = append(p.Warnings, err.Error())
		return
	}
	var file struct {
		MCP      map[string]config.MCPServerConfig `json:"mcpServers"`
		Projects map[string]struct {
			MCP map[string]config.MCPServerConfig `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("parse %s: %v", path, err))
		return
	}
	p.addMCP(path, Global, file.MCP)
	for project, value := range file.Projects {
		if samePath(project, cwd) || pathContains(project, cwd) || pathContains(cwd, project) {
			p.addMCP(path, Project, value.MCP)
		}
	}
}

func (p *Plan) scanMCPFile(path string, scope Scope) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		p.Warnings = append(p.Warnings, err.Error())
		return
	}
	var file struct {
		MCP map[string]config.MCPServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("parse %s: %v", path, err))
		return
	}
	p.addMCP(path, scope, file.MCP)
}

func (p *Plan) addMCP(path string, scope Scope, servers map[string]config.MCPServerConfig) {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p.Items = append(p.Items, Item{Scope: scope, Kind: MCP, Name: name, Source: path, MCP: servers[name]})
	}
}

func (p *Plan) scanDir(path string, scope Scope, kind Kind) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		p.Items = append(p.Items, Item{Scope: scope, Kind: kind, Name: path, Source: path})
	}
}

func (p *Plan) deduplicateOverrides() {
	last := make(map[string]int)
	for i, item := range p.Items {
		if item.Kind == Environment || item.Kind == MCP {
			last[string(item.Scope)+":"+string(item.Kind)+":"+item.Name] = i
		}
	}
	filtered := p.Items[:0]
	for i, item := range p.Items {
		if (item.Kind == Environment || item.Kind == MCP) && last[string(item.Scope)+":"+string(item.Kind)+":"+item.Name] != i {
			continue
		}
		filtered = append(filtered, item)
	}
	p.Items = filtered
}

func Apply(plan Plan, selected map[string]bool) (Result, error) {
	items := make([]Item, 0, len(plan.Items))
	for _, item := range plan.Items {
		if selected == nil || selected[item.ID] {
			items = append(items, item)
		}
	}
	global, project := filterScope(items, Global), filterScope(items, Project)
	globalRoot := os.Getenv("GROK_HOME")
	if globalRoot == "" {
		globalRoot = filepath.Join(plan.Home, ".grok")
	}
	result := Result{}
	count, changed, err := applyScope(globalRoot, global, true)
	if err != nil {
		return result, err
	}
	result.Imported += count
	result.ModifiedFiles = append(result.ModifiedFiles, changed...)
	count, changed, err = applyScope(filepath.Join(plan.ProjectRoot, ".grok"), project, false)
	if err != nil {
		return result, err
	}
	result.Imported += count
	result.ModifiedFiles = append(result.ModifiedFiles, changed...)
	return result, nil
}

func filterScope(items []Item, scope Scope) []Item {
	result := make([]Item, 0, len(items))
	for _, item := range items {
		if item.Scope == scope {
			result = append(result, item)
		}
	}
	return result
}

func hasKind(items []Item, kind Kind) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func applyScope(root string, items []Item, cutoff bool) (int, []string, error) {
	configItems := make([]Item, 0, len(items))
	for _, item := range items {
		if item.Kind == Permission || item.Kind == Environment || item.Kind == MCP {
			configItems = append(configItems, item)
		}
	}
	count, changed, err := applyConfig(filepath.Join(root, "config.toml"), configItems, cutoff)
	if err != nil {
		return 0, nil, err
	}
	files := []string{}
	if changed {
		files = append(files, filepath.Join(root, "config.toml"))
	}
	hookCount, hookChanged, err := applyHooks(filepath.Join(root, "hooks", "imported-from-claude.json"), items)
	if err != nil {
		return 0, nil, err
	}
	count += hookCount
	if hookChanged {
		files = append(files, filepath.Join(root, "hooks", "imported-from-claude.json"))
	}
	for _, item := range items {
		if item.Kind != Skill && item.Kind != Rule {
			continue
		}
		directory := "skills"
		if item.Kind == Rule {
			directory = "rules"
		}
		destination := filepath.Join(root, directory)
		copied, err := copyMissing(item.Source, destination)
		if err != nil {
			return 0, nil, err
		}
		count += copied
		if copied > 0 {
			files = append(files, destination)
		}
	}
	return count, unique(files), nil
}

func applyConfig(path string, items []Item, cutoff bool) (int, bool, error) {
	root := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &root); err != nil {
			return 0, false, fmt.Errorf("parse existing config %s: %w", path, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, false, err
	}
	err = nil
	count := 0
	permissions, env, mcps := map[string]any{}, map[string]any{}, map[string]any{}
	if hasKind(items, Permission) {
		permissions, err = table(root, "permission")
	}
	if err == nil && hasKind(items, Environment) {
		env, err = table(root, "env")
	}
	if err == nil && hasKind(items, MCP) {
		mcps, err = table(root, "mcp_servers")
	}
	if err != nil {
		return 0, false, err
	}
	existingRules := map[string]bool{}
	if raw, ok := permissions["rules"]; ok {
		encoded, _ := json.Marshal(raw)
		var rules []config.PermissionRule
		if json.Unmarshal(encoded, &rules) != nil {
			return 0, false, errors.New("permission.rules is invalid")
		}
		for _, rule := range rules {
			existingRules[ruleKey(rule)] = true
		}
	}
	rules := make([]config.PermissionRule, 0)
	if raw := permissions["rules"]; raw != nil {
		encoded, _ := json.Marshal(raw)
		_ = json.Unmarshal(encoded, &rules)
	}
	for _, item := range items {
		switch item.Kind {
		case Permission:
			if !existingRules[ruleKey(item.Permission)] {
				rules = append(rules, item.Permission)
				existingRules[ruleKey(item.Permission)] = true
				count++
			}
		case Environment:
			if _, exists := env[item.Name]; !exists {
				env[item.Name] = item.Value
				count++
			}
		case MCP:
			if _, exists := mcps[item.Name]; !exists {
				data, _ := json.Marshal(item.MCP)
				var value map[string]any
				_ = json.Unmarshal(data, &value)
				mcps[item.Name] = value
				count++
			}
		}
	}
	if len(rules) > 0 {
		permissions["rules"] = rules
	}
	if cutoff {
		compat, err := table(root, "compat")
		if err != nil {
			return 0, false, err
		}
		claude, err := table(compat, "claude")
		if err != nil {
			return 0, false, err
		}
		for _, key := range []string{"skills", "mcps", "hooks"} {
			if value, ok := claude[key].(bool); !ok || value {
				claude[key] = false
			}
		}
	}
	changed := count > 0 || cutoff && !configAlreadyCutoff(data)
	if !changed {
		return count, false, nil
	}
	encoded, err := toml.Marshal(root)
	if err != nil {
		return 0, false, err
	}
	return count, true, writeAtomic(path, encoded, 0o600)
}

func table(root map[string]any, name string) (map[string]any, error) {
	value, exists := root[name]
	if !exists {
		result := map[string]any{}
		root[name] = result
		return result, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a table", name)
	}
	return result, nil
}

func ruleKey(rule config.PermissionRule) string { data, _ := json.Marshal(rule); return string(data) }

func configAlreadyCutoff(data []byte) bool {
	var file struct {
		Compat struct {
			Claude struct{ Skills, Mcps, Hooks *bool } `toml:"claude"`
		} `toml:"compat"`
	}
	if toml.Unmarshal(data, &file) != nil {
		return false
	}
	return file.Compat.Claude.Skills != nil && !*file.Compat.Claude.Skills && file.Compat.Claude.Mcps != nil && !*file.Compat.Claude.Mcps && file.Compat.Claude.Hooks != nil && !*file.Compat.Claude.Hooks
}

func applyHooks(path string, items []Item) (int, bool, error) {
	newItems := []Item{}
	for _, item := range items {
		if item.Kind == Hook {
			newItems = append(newItems, item)
		}
	}
	if len(newItems) == 0 {
		return 0, false, nil
	}
	root := map[string]any{"hooks": map[string]any{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &root) != nil {
			return 0, false, fmt.Errorf("parse existing hooks %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, false, err
	}
	hooks, err := table(root, "hooks")
	if err != nil {
		return 0, false, err
	}
	count, dirty := 0, false
	for _, item := range newItems {
		groups, _ := hooks[item.Hook.Event].([]any)
		found := false
		for _, raw := range groups {
			group, _ := raw.(map[string]any)
			if group == nil || stringValue(group["matcher"]) != item.Hook.Matcher {
				continue
			}
			handlers, _ := group["hooks"].([]any)
			for _, handlerRaw := range handlers {
				handler, _ := handlerRaw.(map[string]any)
				if handler != nil && stringValue(handler["type"]) == "command" && stringValue(handler["command"]) == item.Hook.Command {
					found = true
					if updateTimeout(handler, item.Hook.Timeout) {
						dirty = true
					}
					break
				}
			}
		}
		if !found {
			handler := map[string]any{"type": "command", "command": item.Hook.Command}
			updateTimeout(handler, item.Hook.Timeout)
			group := map[string]any{"hooks": []any{handler}}
			if item.Hook.Matcher != "" {
				group["matcher"] = item.Hook.Matcher
			}
			hooks[item.Hook.Event] = append(groups, group)
			count++
			dirty = true
		}
	}
	if !dirty {
		return count, false, nil
	}
	encoded, _ := json.MarshalIndent(root, "", "  ")
	encoded = append(encoded, '\n')
	return count, true, writeAtomic(path, encoded, 0o600)
}

func updateTimeout(handler map[string]any, timeout *uint64) bool {
	if timeout == nil {
		if _, ok := handler["timeout"]; ok {
			delete(handler, "timeout")
			return true
		}
		return false
	}
	want := float64(*timeout)
	if handler["timeout"] != want {
		handler["timeout"] = *timeout
		return true
	}
	return false
}

func copyMissing(source, destination string) (int, error) {
	count := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeAtomic(target, data, 0o600); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gork-import-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
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
	return os.Rename(tempPath, path)
}

func stringValue(value any) string { result, _ := value.(string); return result }
func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return filepath.Clean(aa) == filepath.Clean(bb)
}
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func unique(values []string) []string {
	seen := map[string]bool{}
	result := values[:0]
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
