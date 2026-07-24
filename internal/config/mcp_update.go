package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func LoadMCPServersAt(path string) (map[string]MCPServerConfig, []string, error) {
	root, err := readConfigMap(path)
	if err != nil {
		return nil, nil, err
	}
	servers, err := readConfigSection[map[string]MCPServerConfig](root, "mcp_servers")
	if err != nil {
		return nil, nil, err
	}
	disabled := configStringSet(root["disabled_mcp_servers"])
	disabledNames := make([]string, 0, len(disabled))
	for name := range disabled {
		disabledNames = append(disabledNames, name)
	}
	sort.Strings(disabledNames)
	return servers, disabledNames, nil
}

func SetMCPServerEnabled(path, name string, enabled bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("MCP server name is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		disabled := configStringSet(root["disabled_mcp_servers"])
		if enabled {
			delete(disabled, name)
		} else {
			disabled[name] = true
		}
		writeConfigStringSet(root, "disabled_mcp_servers", disabled)
		return nil
	})
}

func SetMCPToolEnabled(path, serverName, toolName string, enabled bool) error {
	serverName, toolName = strings.TrimSpace(serverName), strings.TrimSpace(toolName)
	if serverName == "" || toolName == "" {
		return errors.New("MCP server and tool names are required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		section, err := configMap(root, "disabled_mcp_tools")
		if err != nil {
			return err
		}
		disabled := configStringSet(section[serverName])
		if enabled {
			delete(disabled, toolName)
		} else {
			disabled[toolName] = true
		}
		if len(disabled) == 0 {
			delete(section, serverName)
		} else {
			items := make([]string, 0, len(disabled))
			for name := range disabled {
				items = append(items, name)
			}
			sort.Strings(items)
			section[serverName] = items
		}
		if len(section) == 0 {
			delete(root, "disabled_mcp_tools")
		} else {
			root["disabled_mcp_tools"] = section
		}
		return nil
	})
}

func UpsertMCPServer(path, name string, server MCPServerConfig) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("MCP server name is required")
	}
	if !server.IsEnabled() {
		return errors.New("MCP server config is disabled")
	}
	if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
		return errors.New("MCP server command or URL is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		servers, err := configMap(root, "mcp_servers")
		if err != nil {
			return err
		}
		servers[name] = server
		root["mcp_servers"] = servers
		disabled := configStringSet(root["disabled_mcp_servers"])
		delete(disabled, name)
		writeConfigStringSet(root, "disabled_mcp_servers", disabled)
		return nil
	})
}

func cloneStringSlices(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func DeleteMCPServer(path, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("MCP server name is required")
	}
	existed := false
	err := updateUserConfig(path, func(root map[string]any) error {
		servers, err := configMap(root, "mcp_servers")
		if err != nil {
			return err
		}
		if _, existed = servers[name]; existed {
			delete(servers, name)
			if len(servers) == 0 {
				delete(root, "mcp_servers")
			} else {
				root["mcp_servers"] = servers
			}
		}
		disabled := configStringSet(root["disabled_mcp_servers"])
		delete(disabled, name)
		writeConfigStringSet(root, "disabled_mcp_servers", disabled)
		if tools, ok := root["disabled_mcp_tools"].(map[string]any); ok {
			delete(tools, name)
			if len(tools) == 0 {
				delete(root, "disabled_mcp_tools")
			}
		}
		return nil
	})
	return existed, err
}

func configMap(root map[string]any, key string) (map[string]any, error) {
	value, exists := root[key]
	if !exists {
		return make(map[string]any), nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid %s table", key)
	}
	return result, nil
}

func configStringSet(value any) map[string]bool {
	result := make(map[string]bool)
	items, _ := value.([]any)
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result[text] = true
		}
	}
	return result
}

func writeConfigStringSet(root map[string]any, key string, values map[string]bool) {
	if len(values) == 0 {
		delete(root, key)
		return
	}
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	root[key] = items
}
