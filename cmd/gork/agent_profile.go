package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/subagent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const (
	agentProfileMemoryMaxBytes = 25 * 1024
	agentProfileMemoryMaxLines = 200
)

func loadAgentProfile(path string) (agents.Definition, error) {
	abs, err := canonicalAgentProfilePath(path)
	if err != nil {
		return agents.Definition{}, err
	}
	profile, err := agents.Parse(abs, "")
	if err != nil {
		return agents.Definition{}, fmt.Errorf("failed to load agent profile %q: %w", abs, err)
	}
	profile.Path = abs
	return profile, nil
}

func canonicalAgentProfilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return "", fmt.Errorf("--agent-profile path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("--agent-profile path %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("--agent-profile path is not a file: %s", abs)
	}
	return abs, nil
}

func profileModelRequest(profile *agents.Definition, sessionModel, cliModel string) string {
	if strings.TrimSpace(sessionModel) != "" {
		return sessionModel
	}
	if strings.TrimSpace(cliModel) != "" || profile == nil {
		return cliModel
	}
	return profile.Model
}

func profileEffort(profile *agents.Definition, sessionEffort, cliEffort, fallback string, supported bool) string {
	if sessionEffort != "" && supported {
		return sessionEffort
	}
	if cliEffort != "" && supported {
		return cliEffort
	}
	if profile != nil && profile.Effort != "" && supported {
		if profile.Effort == "max" {
			return "xhigh"
		}
		return profile.Effort
	}
	return fallback
}

func profilePermissionMode(profile *agents.Definition, fallback tools.PermissionMode, cliSet bool) tools.PermissionMode {
	if !cliSet && profile != nil && profile.PermissionMode == "bypassPermissions" {
		return tools.PermissionAlwaysApprove
	}
	return fallback
}

func applyProfileMCP(profile *agents.Definition, cfg *config.Config) ([]mcp.ServerConfig, error) {
	if profile == nil {
		return nil, nil
	}
	inherited := make([]mcp.ServerConfig, 0, len(cfg.MCPServers))
	for name, server := range cfg.MCPServers {
		inherited = append(inherited, mcp.ServerConfig{
			Name: name, Type: server.Type, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers), Disabled: !server.IsEnabled(),
		})
	}
	for name, server := range cfg.MCPServers {
		if !profile.MCPInheritance.Allows(name) {
			disabled := false
			server.Enabled = &disabled
			cfg.MCPServers[name] = server
		}
	}
	return subagent.ResolveProfileMCPServers(*profile, inherited)
}

func applyProfileSkills(profile *agents.Definition, catalog *skills.Catalog, root string, cfg config.Config, plugins []plugin.Plugin) (*skills.Catalog, string, error) {
	if profile == nil {
		return catalog, "", nil
	}
	if !profile.DiscoverSkills {
		return nil, "", nil
	}
	if !profile.InheritSkills {
		skillCfg := workspaceSkillsConfig(cfg, plugins)
		skillCfg.Paths, skillCfg.Ignore, skillCfg.Disabled = nil, nil, nil
		var err error
		catalog, err = skills.Discover(root, skillCfg)
		if err != nil {
			return nil, "", err
		}
	}
	return catalog, catalog.Preload(profile.Skills), nil
}

func applyProfileHooks(profile *agents.Definition, catalog *hooks.Catalog, root string) *hooks.Catalog {
	if profile == nil || len(profile.Hooks) == 0 {
		return catalog
	}
	return catalog.WithInline(profile.Hooks, root, "agent/"+profile.Name+"/", "agent "+profile.Name)
}

func bindProfileMemory(profile *agents.Definition, ws *workspace.Workspace) (*workspace.Workspace, string, error) {
	if profile == nil || profile.Memory == "" {
		return ws, "", nil
	}
	dir, err := profile.MemoryDir(ws.Root())
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create agent profile memory: %w", err)
	}
	bound, err := ws.WithExtraRoot(dir)
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "MEMORY.md")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return bound, "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return bound, "", nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, agentProfileMemoryMaxBytes+utf8.UTFMax))
	if err != nil {
		return bound, "", nil
	}
	if len(data) > agentProfileMemoryMaxBytes {
		limit := agentProfileMemoryMaxBytes
		for limit > 0 && !utf8.Valid(data[:limit]) {
			limit--
		}
		data = data[:limit]
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > agentProfileMemoryMaxLines {
		content = strings.Join(lines[:agentProfileMemoryMaxLines], "\n")
	}
	if !utf8.ValidString(content) || strings.TrimSpace(content) == "" {
		return bound, "", nil
	}
	return bound, "\n\n<agent-memory>\nMemory directory: " + dir + "\n\n" + content + "\n</agent-memory>", nil
}
