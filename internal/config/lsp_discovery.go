package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/lookcorner/go-cli/internal/plugin"
)

// DiscoverLSPServers merges user, project, and enabled plugin LSP configs into
// the already-resolved config. Project config overrides user config; plugins
// only fill names not claimed by either.
func DiscoverLSPServers(workspaceRoot string, cfg Config, plugins []plugin.Plugin, projectTrusted bool) map[string]LSPServerConfig {
	home, _ := os.UserHomeDir()
	return discoverLSPServers(workspaceRoot, home, cfg, plugins, projectTrusted)
}

func discoverLSPServers(workspaceRoot, home string, cfg Config, plugins []plugin.Plugin, projectTrusted bool) map[string]LSPServerConfig {
	workspaceRoot = canonicalOrClean(workspaceRoot)
	servers := cloneLSPServers(cfg.LSPServers)
	if path := userLSPPath(home); path != "" {
		for name, server := range parseLSPJSON(readSmallFile(path)) {
			if _, claimed := servers[name]; !claimed {
				servers[name] = server
			}
		}
	}
	if projectTrusted {
		for name, server := range parseLSPJSON(readSmallFile(filepath.Join(workspaceRoot, ".grok", "lsp.json"))) {
			servers[name] = server
		}
	}
	for _, item := range plugins {
		if !item.Executable {
			continue
		}
		for _, data := range [][]byte{readSmallFile(item.LSPConfig), item.InlineLSP} {
			for name, server := range parseLSPJSON(data) {
				if _, claimed := servers[name]; !claimed {
					servers[name] = server
				}
			}
		}
	}
	return servers
}

func parseLSPJSON(data []byte) map[string]LSPServerConfig {
	if len(data) == 0 {
		return nil
	}
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	result := make(map[string]LSPServerConfig)
	for name, data := range entries {
		if server, ok := parseLSPServer(data); ok {
			result[name] = server
		}
	}
	return result
}

func parseLSPServer(data []byte) (LSPServerConfig, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return LSPServerConfig{}, false
	}
	for _, alias := range [][2]string{
		{"initializationOptions", "initialization_options"},
		{"workspaceFolder", "workspace_folder"},
		{"startupTimeout", "startup_timeout"},
		{"shutdownTimeout", "shutdown_timeout"},
		{"restartOnCrash", "restart_on_crash"},
		{"maxRestarts", "max_restarts"},
		{"extensionToLanguage", "extensions"},
		{"extensionToLanguageId", "extensions"},
	} {
		if _, exists := values[alias[1]]; !exists && values[alias[0]] != nil {
			values[alias[1]] = values[alias[0]]
		}
		delete(values, alias[0])
	}
	extensions := parseLSPExtensions(values["extensions"])
	delete(values, "extensions")
	remaining, err := json.Marshal(values)
	if err != nil {
		return LSPServerConfig{}, false
	}
	var server LSPServerConfig
	if json.Unmarshal(remaining, &server) != nil {
		return LSPServerConfig{}, false
	}
	server.Extensions = extensions
	return server, true
}

func parseLSPExtensions(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(data, &list) == nil {
		return list
	}
	var mapping map[string]string
	if json.Unmarshal(data, &mapping) != nil {
		return nil
	}
	list = make([]string, 0, len(mapping))
	for extension := range mapping {
		list = append(list, extension)
	}
	sort.Strings(list)
	return list
}

func userLSPPath(home string) string {
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" {
		if home == "" {
			return ""
		}
		grokHome = filepath.Join(home, ".grok")
	} else if !filepath.IsAbs(grokHome) {
		grokHome, _ = filepath.Abs(grokHome)
	}
	return filepath.Join(grokHome, "lsp.json")
}

func cloneLSPServers(source map[string]LSPServerConfig) map[string]LSPServerConfig {
	result := make(map[string]LSPServerConfig, len(source))
	for name, server := range source {
		result[name] = server
	}
	return result
}
