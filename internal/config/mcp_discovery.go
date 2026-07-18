package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// DiscoverMCPServers merges workspace and compatible editor MCP sources into
// the already-resolved config. TOML wins over non-TOML sources, with closer
// project TOML replacing global entries.
func DiscoverMCPServers(workspaceRoot string, cfg Config, plugins []plugin.Plugin) map[string]MCPServerConfig {
	home, _ := os.UserHomeDir()
	return discoverMCPServers(workspaceRoot, home, cfg, plugins)
}

func discoverMCPServers(workspaceRoot, home string, cfg Config, plugins []plugin.Plugin) map[string]MCPServerConfig {
	workspaceRoot = canonicalOrClean(workspaceRoot)
	servers := cloneMCPServers(cfg.MCPServers)

	gitRoot := workspace.GitRoot(workspaceRoot)
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		for name, server := range readProjectMCP(filepath.Join(scope, ".grok", "config.toml")) {
			servers[name] = server
		}
	}

	for _, item := range plugins {
		for _, source := range []struct {
			data        []byte
			allowDirect bool
		}{
			{data: readSmallFile(item.MCPConfig)},
			{data: item.InlineMCP, allowDirect: true},
		} {
			for name, server := range parseMCPJSON(source.data, source.allowDirect) {
				if _, claimed := servers[name]; claimed || !server.IsEnabled() {
					continue
				}
				servers[name] = expandMCPServer(server, item.Root, item.DataDir)
			}
		}
	}

	if cfg.Compat.Claude.Mcps && home != "" {
		for name, server := range readClaudeMCP(filepath.Join(home, ".claude.json"), workspaceRoot) {
			if _, claimed := servers[name]; !claimed {
				servers[name] = server
			}
		}
	}
	if cfg.Compat.Cursor.Mcps {
		paths := []string{filepath.Join(workspaceRoot, ".cursor", "mcp.json")}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".cursor", "mcp.json"))
		}
		for _, path := range paths {
			for name, server := range parseMCPJSON(readSmallFile(path), false) {
				if _, claimed := servers[name]; !claimed {
					servers[name] = server
				}
			}
		}
	}

	local := make(map[string]MCPServerConfig)
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		for name, server := range parseMCPJSON(readSmallFile(filepath.Join(scope, ".mcp.json")), false) {
			local[name] = server
		}
	}
	for name, server := range local {
		if _, claimed := servers[name]; !claimed {
			servers[name] = server
		}
	}

	for name, server := range servers {
		server = expandMCPServer(server, "", "")
		if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
			disabled := false
			server.Enabled = &disabled
		}
		servers[name] = server
	}
	return servers
}

func readProjectMCP(path string) map[string]MCPServerConfig {
	data := readSmallFile(path)
	if len(data) == 0 {
		return nil
	}
	var file struct {
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	if toml.Unmarshal(data, &file) != nil {
		return nil
	}
	result := make(map[string]MCPServerConfig)
	for name, raw := range file.MCPServers {
		data, err := toml.Marshal(map[string]any{"server": raw})
		if err != nil {
			continue
		}
		var entry struct {
			Server MCPServerConfig `toml:"server"`
		}
		if toml.Unmarshal(data, &entry) == nil {
			result[name] = entry.Server
		}
	}
	return result
}

func readClaudeMCP(path, workspaceRoot string) map[string]MCPServerConfig {
	data := readSmallFile(path)
	if len(data) == 0 {
		return nil
	}
	var file struct {
		MCPServers map[string]MCPServerConfig `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]MCPServerConfig `json:"mcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	result := make(map[string]MCPServerConfig)
	if project, ok := file.Projects[workspaceRoot]; ok {
		for name, server := range project.MCPServers {
			result[name] = server
		}
	}
	for name, server := range file.MCPServers {
		if _, claimed := result[name]; !claimed {
			result[name] = server
		}
	}
	return result
}

func parseMCPJSON(data []byte, allowDirect bool) map[string]MCPServerConfig {
	if len(data) == 0 {
		return nil
	}
	var wrapped struct {
		MCPServers map[string]MCPServerConfig `json:"mcpServers"`
	}
	if json.Unmarshal(data, &wrapped) != nil {
		return nil
	}
	if wrapped.MCPServers != nil || !allowDirect {
		return wrapped.MCPServers
	}
	var direct map[string]MCPServerConfig
	if json.Unmarshal(data, &direct) != nil {
		return nil
	}
	return direct
}

func expandMCPServer(server MCPServerConfig, pluginRoot, pluginData string) MCPServerConfig {
	expand := func(value string) string {
		if pluginRoot != "" {
			value = strings.ReplaceAll(value, "${GROK_PLUGIN_ROOT}", pluginRoot)
			value = strings.ReplaceAll(value, "${CLAUDE_PLUGIN_ROOT}", pluginRoot)
		}
		if pluginData != "" {
			value = strings.ReplaceAll(value, "${GROK_PLUGIN_DATA}", pluginData)
			value = strings.ReplaceAll(value, "${CLAUDE_PLUGIN_DATA}", pluginData)
		}
		return expandEnv(value)
	}
	server.Command = expand(server.Command)
	server.URL = expand(server.URL)
	server.Args = append([]string(nil), server.Args...)
	for index := range server.Args {
		server.Args[index] = expand(server.Args[index])
	}
	server.Env = cloneStringMap(server.Env)
	for name, value := range server.Env {
		server.Env[name] = expand(value)
	}
	server.Headers = cloneStringMap(server.Headers)
	for name, value := range server.Headers {
		server.Headers[name] = expand(value)
	}
	return server
}

func expandEnv(value string) string {
	for index := 0; index < len(value); {
		if value[index] != '$' {
			index++
			continue
		}
		start, end := index+1, index+1
		if start < len(value) && value[start] == '{' {
			start++
			end = strings.IndexByte(value[start:], '}')
			if end < 0 {
				index++
				continue
			}
			end += start
		} else {
			for end < len(value) && (value[end] == '_' || value[end] >= 'a' && value[end] <= 'z' || value[end] >= 'A' && value[end] <= 'Z' || value[end] >= '0' && value[end] <= '9') {
				end++
			}
			if end == start {
				index++
				continue
			}
		}
		name := value[start:end]
		replacement, ok := os.LookupEnv(name)
		if !ok {
			index = end
			continue
		}
		tokenEnd := end
		if value[index+1] == '{' {
			tokenEnd++
		}
		value = value[:index] + replacement + value[tokenEnd:]
		index += len(replacement)
	}
	return value
}

func readSmallFile(path string) []byte {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

func cloneMCPServers(source map[string]MCPServerConfig) map[string]MCPServerConfig {
	result := make(map[string]MCPServerConfig, len(source))
	for name, server := range source {
		result[name] = server
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func canonicalOrClean(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
