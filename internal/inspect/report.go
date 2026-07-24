package inspect

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type Report struct {
	GorkVersion       string         `json:"gorkVersion"`
	CWD               string         `json:"cwd"`
	ProjectRoot       string         `json:"projectRoot,omitempty"`
	ProjectTrusted    bool           `json:"projectTrusted"`
	Instructions      []Instruction  `json:"projectInstructions"`
	Permissions       Permissions    `json:"permissions"`
	LoginPolicy       LoginPolicy    `json:"loginPolicy"`
	Hooks             []Hook         `json:"hooks"`
	Skills            []Skill        `json:"skills"`
	Agents            []Agent        `json:"agents"`
	Plugins           []Plugin       `json:"plugins"`
	Marketplaces      []Marketplace  `json:"marketplaces"`
	MCPServers        []MCPServer    `json:"mcpServers"`
	LSPServers        []LSPServer    `json:"lspServers"`
	ConfigSources     []ConfigSource `json:"configSources"`
	DiscoveryWarnings []string       `json:"discoveryWarnings,omitempty"`
}

type Instruction struct {
	Path         string `json:"path"`
	SizeBytes    int    `json:"sizeBytes"`
	ApproxTokens int    `json:"approxTokens"`
}

type Permissions struct {
	Mode           string `json:"mode"`
	Rules          int    `json:"rules"`
	BypassDisabled bool   `json:"bypassDisabled"`
}

type LoginPolicy struct {
	PreferredMethod    string   `json:"preferredMethod,omitempty"`
	APIKeyAuthDisabled bool     `json:"apiKeyAuthDisabled"`
	ForceLoginTeams    []string `json:"forceLoginTeams,omitempty"`
}

type Hook struct {
	Name     string `json:"name"`
	Event    string `json:"event"`
	Type     string `json:"type"`
	Matcher  string `json:"matcher,omitempty"`
	Disabled bool   `json:"disabled"`
}

type Skill struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Source        string `json:"source"`
	Path          string `json:"path"`
	UserInvocable bool   `json:"userInvocable"`
	Enabled       bool   `json:"enabled"`
}

type Agent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
	Builtin     bool   `json:"builtin"`
}

type Plugin struct {
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Path       string `json:"path"`
	Enabled    bool   `json:"enabled"`
	Executable bool   `json:"executable"`
	Skills     int    `json:"skills"`
	Agents     int    `json:"agents"`
	Hooks      bool   `json:"hooks"`
	MCP        bool   `json:"mcp"`
	LSP        bool   `json:"lsp"`
}

type Marketplace struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type MCPServer struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Enabled   bool   `json:"enabled"`
}

type LSPServer struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Extensions []string `json:"extensions,omitempty"`
	Enabled    bool     `json:"enabled"`
}

type ConfigSource struct {
	Path string `json:"path"`
	Role string `json:"role"`
}

func Build(cwd, configPath string) (Report, error) {
	ws, err := workspace.Open(cwd)
	if err != nil {
		return Report{}, err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return Report{}, err
	}
	trusted := workspace.ResolveFolderTrust(ws.Root(), cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
	inventory, err := plugin.Inventory(ws.Root(), plugin.Config{
		Paths: cfg.Plugins.Paths, Enabled: cfg.Plugins.Enabled, Disabled: cfg.Plugins.Disabled,
		ProjectTrusted: trusted,
	})
	if err != nil {
		return Report{}, err
	}
	enabled := enabledPlugins(inventory)
	instructions, err := ws.LoadInstructions(cfg.Compat)
	if err != nil {
		return Report{}, err
	}
	skillCatalog, err := skills.Discover(ws.Root(), skills.Config{
		Compat: cfg.Compat, Paths: cfg.Skills.Paths, Ignore: cfg.Skills.Ignore,
		Disabled: cfg.Skills.Disabled, Plugins: enabled,
	})
	if err != nil {
		return Report{}, err
	}
	agentSettings, err := config.LoadAgentSettings(configPath)
	if err != nil {
		return Report{}, err
	}
	agentCatalog, agentErrors := agents.Discover(agents.Config{
		WorkspaceRoot: ws.Root(), ProjectTrusted: trusted, Compat: cfg.Compat, Plugins: enabled,
		Toggles: agentSettings.Toggle,
	})
	hookSnapshot := hooks.Discover(hooks.Config{
		WorkspaceRoot: ws.Root(), ProjectTrusted: trusted, Compat: cfg.Compat, Plugins: enabled,
	}).Snapshot()
	marketplaces, err := marketplace.Sources(configPath, ws.Root())
	if err != nil {
		return Report{}, err
	}
	mcpServers := config.DiscoverMCPServers(ws.Root(), cfg, enabled, trusted)
	lspServers := config.DiscoverLSPServers(ws.Root(), cfg, enabled, trusted)

	report := Report{
		GorkVersion: version.Current, CWD: ws.Root(), ProjectTrusted: trusted,
		Permissions: Permissions{
			Mode: cfg.UI.PermissionMode, Rules: len(cfg.Permission.Rules),
			BypassDisabled: cfg.DisableBypassPermissionsMode,
		},
		LoginPolicy: LoginPolicy{
			PreferredMethod:    cfg.PreferredAuthMethod,
			APIKeyAuthDisabled: cfg.DisableAPIKeyAuth || cfg.ForceLoginTeamConfigured,
			ForceLoginTeams:    append([]string(nil), cfg.ForceLoginTeams...),
		},
		Instructions:  inspectInstructions(instructions),
		Hooks:         inspectHooks(hookSnapshot.Hooks),
		Skills:        inspectSkills(skillCatalog.List()),
		Agents:        inspectAgents(agentCatalog.Definitions()),
		Plugins:       inspectPlugins(inventory),
		Marketplaces:  inspectMarketplaces(marketplaces),
		MCPServers:    inspectMCPServers(mcpServers),
		LSPServers:    inspectLSPServers(lspServers),
		ConfigSources: inspectConfigSources(ws.Root(), configPath),
	}
	if root, ok := workspace.FindGitRoot(ws.Root()); ok {
		report.ProjectRoot = root
	}
	report.DiscoveryWarnings = append(report.DiscoveryWarnings, hookSnapshot.LoadErrors...)
	for _, item := range agentErrors {
		report.DiscoveryWarnings = append(report.DiscoveryWarnings, item.Error())
	}
	sort.Strings(report.DiscoveryWarnings)
	return report, nil
}

func enabledPlugins(inventory []plugin.Plugin) []plugin.Plugin {
	var result []plugin.Plugin
	for _, item := range inventory {
		if item.Enabled {
			result = append(result, item)
		}
	}
	return result
}

func inspectInstructions(values []workspace.InstructionFile) []Instruction {
	result := make([]Instruction, 0, len(values))
	for _, item := range values {
		result = append(result, Instruction{Path: item.Path, SizeBytes: len(item.Content), ApproxTokens: (len(item.Content) + 3) / 4})
	}
	return result
}

func inspectHooks(values []hooks.Spec) []Hook {
	result := make([]Hook, 0, len(values))
	for _, item := range values {
		result = append(result, Hook{Name: item.Name, Event: string(item.Event), Type: item.Type, Matcher: item.Matcher, Disabled: item.Disabled})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func inspectSkills(values []skills.Info) []Skill {
	result := make([]Skill, 0, len(values))
	for _, item := range values {
		result = append(result, Skill{
			Name: item.Name, Description: item.Description, Source: item.Scope, Path: item.Path,
			UserInvocable: item.UserInvocable, Enabled: item.Enabled,
		})
	}
	return result
}

func inspectAgents(values []agents.Definition) []Agent {
	result := make([]Agent, 0, len(values))
	for _, item := range values {
		result = append(result, Agent{
			Name: item.Name, Description: item.Description, Scope: item.Scope,
			Enabled: item.Enabled, Builtin: item.Builtin,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func inspectPlugins(values []plugin.Plugin) []Plugin {
	result := make([]Plugin, 0, len(values))
	for _, item := range values {
		result = append(result, Plugin{
			Name: item.Name, Scope: item.Scope, Path: item.Root, Enabled: item.Enabled,
			Executable: item.Executable, Skills: len(item.SkillDirs) + len(item.CommandDirs),
			Agents: len(item.AgentDirs), Hooks: item.HooksConfig != "" || len(item.InlineHooks) > 0,
			MCP: item.MCPConfig != "" || len(item.InlineMCP) > 0,
			LSP: item.LSPConfig != "" || len(item.InlineLSP) > 0,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func inspectMarketplaces(values []marketplace.Source) []Marketplace {
	result := make([]Marketplace, 0, len(values))
	for _, item := range values {
		kind, path := "local", item.Path
		if item.Git != "" {
			kind, path = "git", redactURLCredentials(item.Git)
		}
		result = append(result, Marketplace{Name: item.Name, Kind: kind, Path: path})
	}
	return result
}

func redactURLCredentials(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func inspectMCPServers(values map[string]config.MCPServerConfig) []MCPServer {
	result := make([]MCPServer, 0, len(values))
	for name, item := range values {
		transport := item.Type
		if transport == "" {
			if item.URL != "" {
				transport = "http"
			} else {
				transport = "stdio"
			}
		}
		result = append(result, MCPServer{Name: name, Transport: transport, Enabled: item.IsEnabled()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func inspectLSPServers(values map[string]config.LSPServerConfig) []LSPServer {
	result := make([]LSPServer, 0, len(values))
	for name, item := range values {
		result = append(result, LSPServer{
			Name: name, Command: filepath.Base(item.Command),
			Extensions: append([]string(nil), item.Extensions...), Enabled: item.IsEnabled(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func inspectConfigSources(root, configPath string) []ConfigSource {
	paths := config.ModelWatchPaths(configPath)
	for _, scope := range workspace.ProjectScopes(workspace.GitRoot(root), root) {
		paths = append(paths, filepath.Join(scope, ".grok", "config.toml"))
	}
	seen := make(map[string]bool)
	result := make([]ConfigSource, 0)
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		role := "config"
		switch strings.ToLower(filepath.Base(path)) {
		case "managed_config.toml":
			role = "managed"
		case "requirements.toml":
			role = "requirements"
		}
		result = append(result, ConfigSource{Path: path, Role: role})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
