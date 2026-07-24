package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type Scope string

const (
	UserScope       Scope = "user"
	ProjectScope    Scope = "project"
	DiscoveredScope Scope = "discovered"
)

type Entry struct {
	Name       string                 `json:"name"`
	Scope      Scope                  `json:"scope"`
	ConfigPath string                 `json:"-"`
	Config     config.MCPServerConfig `json:"-"`
	Blocked    string                 `json:"-"`
}

func (e Entry) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(e.Config)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	result["name"], result["scope"] = e.Name, e.Scope
	return json.Marshal(result)
}

type AddRequest struct {
	Name      string
	Scope     Scope
	Transport string
	Source    string
	Args      []string
	Env       map[string]string
	Headers   map[string]string
}

type DoctorEntry struct {
	Name      string  `json:"name"`
	Transport string  `json:"transport"`
	Target    string  `json:"target"`
	Source    string  `json:"source"`
	Checks    []Check `json:"checks"`
	Healthy   bool    `json:"healthy"`
}

type Check struct {
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type SourceStatus struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	ServerCount int    `json:"serverCount,omitempty"`
}

type DoctorReport struct {
	Sources      []SourceStatus `json:"sources"`
	Servers      []DoctorEntry  `json:"servers"`
	HealthyCount int            `json:"healthyCount"`
	FailingCount int            `json:"failingCount"`
}

type ProbeResult struct {
	ProtocolVersion string
	ServerName      string
	ServerVersion   string
	ToolCount       int
}

type ProbeFunc func(context.Context, string, config.MCPServerConfig, string) (ProbeResult, error)

func List(cwd, userConfigPath string) ([]Entry, error) {
	userConfigPath, err := resolveUserConfigPath(userConfigPath)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]Entry)
	if err := mergeConfig(entries, userConfigPath, UserScope); err != nil {
		return nil, err
	}
	for _, scope := range workspace.ProjectScopes(workspace.GitRoot(cwd), cwd) {
		path := filepath.Join(scope, ".grok", "config.toml")
		if err := mergeConfig(entries, path, ProjectScope); err != nil {
			return nil, err
		}
	}
	return sortedEntries(entries), nil
}

func Add(cwd, userConfigPath string, request AddRequest) (string, error) {
	if err := validateName(request.Name); err != nil {
		return "", err
	}
	target, err := scopePath(cwd, userConfigPath, request.Scope)
	if err != nil {
		return "", err
	}
	server, err := buildServer(request)
	if err != nil {
		return "", err
	}
	if err := config.UpsertMCPServer(target, request.Name, server); err != nil {
		return "", err
	}
	return target, nil
}

func Remove(cwd, userConfigPath, name string, requested Scope) (Scope, string, error) {
	if err := validateName(name); err != nil {
		return "", "", err
	}
	userPath, err := resolveUserConfigPath(userConfigPath)
	if err != nil {
		return "", "", err
	}
	userDefined, err := definedAt(userPath, name)
	if err != nil {
		return "", "", err
	}
	projectPath, err := nearestProjectDefinition(cwd, name)
	if err != nil {
		return "", "", err
	}
	var scope Scope
	var target string
	switch requested {
	case UserScope:
		scope, target = UserScope, userPath
		if !userDefined {
			return "", "", fmt.Errorf("MCP server %q is not defined in user config", name)
		}
	case ProjectScope:
		scope, target = ProjectScope, projectPath
		if target == "" {
			return "", "", fmt.Errorf("MCP server %q is not defined in project config", name)
		}
	case "":
		switch {
		case userDefined && projectPath != "":
			return "", "", fmt.Errorf("MCP server %q exists in user and project configs; specify --scope", name)
		case userDefined:
			scope, target = UserScope, userPath
		case projectPath != "":
			scope, target = ProjectScope, projectPath
		default:
			return "", "", fmt.Errorf("MCP server %q was not found", name)
		}
	default:
		return "", "", fmt.Errorf("invalid MCP scope %q", requested)
	}
	existed, err := config.DeleteMCPServer(target, name)
	if err != nil {
		return "", "", err
	}
	if !existed {
		return "", "", fmt.Errorf("MCP server %q was not found", name)
	}
	return scope, target, nil
}

func RemainingDefinition(cwd, userConfigPath, name string) (Scope, string, bool, error) {
	userPath, err := resolveUserConfigPath(userConfigPath)
	if err != nil {
		return "", "", false, err
	}
	projectPath, err := nearestProjectDefinition(cwd, name)
	if err != nil {
		return "", "", false, err
	}
	if projectPath != "" {
		return ProjectScope, projectPath, true, nil
	}
	defined, err := definedAt(userPath, name)
	if err != nil {
		return "", "", false, err
	}
	if defined {
		return UserScope, userPath, true, nil
	}
	return "", "", false, nil
}

func Doctor(ctx context.Context, cwd, userConfigPath, name string, probe ProbeFunc) (DoctorReport, error) {
	entries, err := effectiveEntries(cwd, userConfigPath)
	if err != nil {
		return DoctorReport{}, err
	}
	sources := doctorSources(entries)
	if name != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Name == name {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			return DoctorReport{}, fmt.Errorf("MCP server %q was not found", name)
		}
	}
	if probe == nil {
		probe = Probe
	}
	report := DoctorReport{Sources: sources, Servers: make([]DoctorEntry, 0, len(entries))}
	for _, entry := range entries {
		item := DoctorEntry{
			Name: entry.Name, Transport: transport(entry.Config), Target: target(entry.Config),
			Source: string(entry.Scope), Checks: make([]Check, 0, 2),
		}
		if entry.Blocked != "" {
			item.Checks = append(item.Checks, Check{
				Label: "folder untrusted", Detail: entry.Blocked,
				Hint: "trust the folder to allow repo-local MCP servers",
			})
			report.FailingCount++
			report.Servers = append(report.Servers, item)
			continue
		}
		if !entry.Config.IsEnabled() {
			item.Checks = append(item.Checks, Check{
				Label: "disabled in config", Detail: "server is disabled in config.toml",
				Hint: "set enabled = true or remove from disabled_mcp_servers",
			})
			report.FailingCount++
			report.Servers = append(report.Servers, item)
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		result, probeErr := probe(probeCtx, entry.Name, entry.Config, cwd)
		cancel()
		if probeErr != nil {
			item.Checks = append(item.Checks, Check{Label: "handshake failed", Detail: sanitizeError(probeErr.Error())})
			report.FailingCount++
		} else {
			item.Healthy = true
			item.Checks = append(item.Checks,
				Check{Label: "handshake", Passed: true, Detail: result.ProtocolVersion},
				Check{Label: "tools discovered", Passed: true, Detail: fmt.Sprintf("%d tools", result.ToolCount)},
			)
			report.HealthyCount++
		}
		report.Servers = append(report.Servers, item)
	}
	return report, nil
}

func Probe(ctx context.Context, name string, server config.MCPServerConfig, cwd string) (ProbeResult, error) {
	var client *mcp.Client
	var initialized mcp.InitializeResult
	var err error
	switch transport(server) {
	case "stdio":
		client, initialized, err = mcp.Start(ctx, mcp.ProcessConfig{
			Name: name, Command: server.Command, Args: server.Args, Env: server.Env, Dir: cwd,
			Stderr: io.Discard,
		})
	case "http":
		client, initialized, err = mcp.StartHTTP(ctx, mcp.HTTPConfig{
			Name: name, URL: server.URL, Headers: serverHeaders(server), Client: &http.Client{Timeout: 25 * time.Second},
		})
	case "sse":
		client, initialized, err = mcp.StartSSE(ctx, mcp.HTTPConfig{
			Name: name, URL: server.URL, Headers: serverHeaders(server), Client: &http.Client{Timeout: 25 * time.Second},
		})
	default:
		return ProbeResult{}, fmt.Errorf("unsupported MCP transport %q", server.Type)
	}
	if err != nil {
		return ProbeResult{}, err
	}
	defer client.Close()
	tools, err := client.ListTools(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{
		ProtocolVersion: initialized.ProtocolVersion,
		ServerName:      initialized.ServerInfo.Name, ServerVersion: initialized.ServerInfo.Version,
		ToolCount: len(tools),
	}, nil
}

func mergeConfig(entries map[string]Entry, path string, scope Scope) error {
	servers, disabled, err := config.LoadMCPServersAt(path)
	if err != nil {
		return err
	}
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}
	for name, server := range servers {
		if disabledSet[name] {
			enabled := false
			server.Enabled = &enabled
		}
		entries[name] = Entry{Name: name, Scope: scope, ConfigPath: path, Config: server}
	}
	return nil
}

func effectiveEntries(cwd, userConfigPath string) ([]Entry, error) {
	scoped, err := List(cwd, userConfigPath)
	if err != nil {
		return nil, err
	}
	scopeByName := make(map[string]Entry, len(scoped))
	for _, entry := range scoped {
		scopeByName[entry.Name] = entry
	}
	cfg, err := config.Load(userConfigPath)
	if err != nil {
		return nil, err
	}
	trusted := workspace.ResolveFolderTrust(cwd, cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
	inventory, err := plugin.Inventory(cwd, plugin.Config{
		Paths: cfg.Plugins.Paths, Enabled: cfg.Plugins.Enabled, Disabled: cfg.Plugins.Disabled, ProjectTrusted: trusted,
	})
	if err != nil {
		return nil, err
	}
	enabledPlugins := make([]plugin.Plugin, 0, len(inventory))
	for _, item := range inventory {
		if item.Enabled {
			enabledPlugins = append(enabledPlugins, item)
		}
	}
	servers := config.DiscoverMCPServers(cwd, cfg, enabledPlugins, trusted)
	result := make([]Entry, 0, len(servers))
	seen := make(map[string]bool, len(servers))
	for name, server := range servers {
		entry, ok := scopeByName[name]
		if !ok {
			entry = Entry{Name: name, Scope: DiscoveredScope}
		}
		entry.Config = server
		result = append(result, entry)
		seen[name] = true
	}
	if !trusted {
		for name, entry := range scopeByName {
			if seen[name] || entry.Scope != ProjectScope {
				continue
			}
			entry.Blocked = "repo-local server not started for an untrusted folder"
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func sortedEntries(values map[string]Entry) []Entry {
	result := make([]Entry, 0, len(values))
	for _, entry := range values {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func doctorSources(entries []Entry) []SourceStatus {
	counts := make(map[string]int)
	for _, entry := range entries {
		path := entry.ConfigPath
		if path == "" {
			path = string(entry.Scope)
		}
		counts[path]++
	}
	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]SourceStatus, 0, len(paths))
	for _, path := range paths {
		result = append(result, SourceStatus{Path: path, Status: "found", ServerCount: counts[path]})
	}
	return result
}

func scopePath(cwd, userConfigPath string, scope Scope) (string, error) {
	switch scope {
	case "", UserScope:
		return resolveUserConfigPath(userConfigPath)
	case ProjectScope:
		return filepath.Join(cwd, ".grok", "config.toml"), nil
	default:
		return "", fmt.Errorf("invalid MCP scope %q", scope)
	}
}

func resolveUserConfigPath(path string) (string, error) {
	if path != "" {
		return filepath.Abs(path)
	}
	path, err := config.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func nearestProjectDefinition(cwd, name string) (string, error) {
	scopes := workspace.ProjectScopes(workspace.GitRoot(cwd), cwd)
	for index := len(scopes) - 1; index >= 0; index-- {
		path := filepath.Join(scopes[index], ".grok", "config.toml")
		defined, err := definedAt(path, name)
		if err != nil {
			return "", err
		}
		if defined {
			return path, nil
		}
	}
	return "", nil
}

func definedAt(path, name string) (bool, error) {
	servers, _, err := config.LoadMCPServersAt(path)
	if err != nil {
		return false, err
	}
	_, ok := servers[name]
	return ok, nil
}

func buildServer(request AddRequest) (config.MCPServerConfig, error) {
	kind := strings.ToLower(strings.TrimSpace(request.Transport))
	if kind == "" {
		kind = "stdio"
	}
	switch kind {
	case "stdio":
		if strings.TrimSpace(request.Source) == "" {
			return config.MCPServerConfig{}, errors.New("stdio MCP server command is required")
		}
		if len(request.Headers) > 0 {
			return config.MCPServerConfig{}, errors.New("headers are only valid for HTTP or SSE MCP servers")
		}
		return config.MCPServerConfig{
			Command: request.Source, Args: append([]string(nil), request.Args...), Env: request.Env,
		}, nil
	case "http", "sse":
		parsed, err := url.Parse(request.Source)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return config.MCPServerConfig{}, fmt.Errorf("invalid MCP server URL %q", request.Source)
		}
		if len(request.Args) > 0 {
			return config.MCPServerConfig{}, errors.New("HTTP and SSE MCP servers do not accept command arguments")
		}
		if len(request.Env) > 0 {
			return config.MCPServerConfig{}, errors.New("environment variables are only valid for stdio MCP servers")
		}
		return config.MCPServerConfig{URL: request.Source, Type: kind, Headers: request.Headers}, nil
	default:
		return config.MCPServerConfig{}, fmt.Errorf("invalid MCP transport %q", request.Transport)
	}
}

func validateName(name string) error {
	if name == "" {
		return errors.New("MCP server name is required")
	}
	for _, char := range name {
		if char < 'a' || char > 'z' {
			if char < 'A' || char > 'Z' {
				if char < '0' || char > '9' {
					if char != '-' && char != '_' {
						return fmt.Errorf("invalid MCP server name %q", name)
					}
				}
			}
		}
	}
	return nil
}

func transport(server config.MCPServerConfig) string {
	if kind := strings.ToLower(strings.TrimSpace(server.Type)); kind != "" {
		return kind
	}
	if server.URL != "" {
		return "http"
	}
	return "stdio"
}

func target(server config.MCPServerConfig) string {
	if server.URL != "" {
		parsed, err := url.Parse(server.URL)
		if err == nil {
			parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
			return parsed.String()
		}
		return "(invalid URL)"
	}
	return filepath.Base(server.Command)
}

func serverHeaders(server config.MCPServerConfig) map[string]string {
	headers := make(map[string]string, len(server.Headers)+1)
	for name, value := range server.Headers {
		headers[name] = value
	}
	if server.BearerTokenEnvVar != "" {
		if token := os.Getenv(server.BearerTokenEnvVar); token != "" {
			headers["Authorization"] = "Bearer " + token
		}
	}
	return headers
}

func sanitizeError(message string) string {
	for _, raw := range strings.Fields(message) {
		parsed, err := url.Parse(strings.Trim(raw, `"'(),`))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		message = strings.ReplaceAll(message, raw, parsed.String())
	}
	return message
}
