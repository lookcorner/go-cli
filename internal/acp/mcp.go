package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/config"
	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type callableMCPTool interface {
	MCPIdentity() (string, string, mcppkg.ToolInfo)
	CallMCP(context.Context, json.RawMessage) (mcppkg.ToolResult, error)
}

type readableMCPResource interface {
	MCPResourceReader() (string, bool)
	ReadMCPResource(context.Context, string) ([]mcppkg.ResourceContents, error)
}

func parseMCPSDKServers(meta map[string]any) []MCPServer {
	entries, _ := meta["x.ai/mcp/servers"].([]any)
	servers := make([]MCPServer, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, nameOK := entry["name"].(string)
		serverID, idOK := entry["serverId"].(string)
		name, serverID = strings.TrimSpace(name), strings.TrimSpace(serverID)
		if !nameOK || !idOK || name == "" || serverID == "" || seen[name] {
			continue
		}
		seen[name] = true
		servers = append(servers, MCPServer{Type: "acp", Name: name, ServerID: serverID})
	}
	return servers
}

func (s *Server) callMCPSDK(ctx context.Context, serverID string, payload json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("gork-mcp-%d", s.nextRequest.Add(1))
	result := make(chan mcpReverseResult, 1)
	s.mu.Lock()
	if s.pendingMCP == nil {
		s.pendingMCP = make(map[string]chan mcpReverseResult)
	}
	s.pendingMCP[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingMCP, id)
		s.mu.Unlock()
	}()
	if !s.writeResult(map[string]any{"jsonrpc": "2.0", "id": id, "method": "x.ai/mcp/sdk_call", "params": map[string]any{
		"serverId": serverID, "message": payload,
	}}) {
		return nil, io.ErrClosedPipe
	}
	select {
	case response := <-result:
		return response.result, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NotifyMCPServerChanges publishes the state transitions caused by a
// successfully applied MCP configuration replacement. The runtime calls this
// only after the new clients are ready, so the ready transition is authoritative.
func (s *Server) NotifyMCPServerChanges(sessionID string, before, after []MCPServer) {
	previous := make(map[string]MCPServer, len(before))
	next := make(map[string]MCPServer, len(after))
	for _, server := range before {
		previous[server.Name] = server
	}
	for _, server := range after {
		next[server.Name] = server
	}
	names := make([]string, 0, len(previous)+len(next))
	seen := make(map[string]bool, len(previous)+len(next))
	for name := range previous {
		seen[name] = true
		names = append(names, name)
	}
	for name := range next {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		old, hadOld := previous[name]
		current, hasCurrent := next[name]
		if hadOld && hasCurrent && mcpServerTransportEqual(old, current) {
			continue
		}
		switch {
		case !hasCurrent:
			s.notifyMCPServerStatus(sessionID, name, "unavailable", "config_removed")
		case current.Disabled:
			reason := "disabled"
			if !hadOld {
				reason = "config_added"
			}
			s.notifyMCPServerStatus(sessionID, name, "unavailable", reason)
		case !hadOld || old.Disabled:
			s.notifyMCPServerStatus(sessionID, name, "initializing", "config_added")
			s.notifyMCPServerStatus(sessionID, name, "ready", "initialized")
		default:
			s.notifyMCPServerStatus(sessionID, name, "unavailable", "config_removed")
			s.notifyMCPServerStatus(sessionID, name, "initializing", "config_added")
			s.notifyMCPServerStatus(sessionID, name, "ready", "initialized")
		}
	}
	s.NotifyMCPServersUpdated(sessionID)
}

func (s *Server) notifyMCPServerStatus(sessionID, name, status, reason string) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp/server_status", "params": map[string]any{
		"sessionId": sessionID, "name": name, "source": "local", "status": status, "reason": reason, "tools": nil,
	}})
}

// NotifyMCPToolsChanged publishes a refreshed tool directory after an MCP
// server sends notifications/tools/list_changed.
func (s *Server) NotifyMCPToolsChanged(sessionID, serverName string, tools []mcppkg.ToolInfo) {
	entries := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		entry := map[string]any{"name": tool.Name, "enabled": true}
		if tool.Title != "" {
			entry["displayName"] = tool.Title
		}
		if tool.Description != "" {
			entry["description"] = tool.Description
		}
		if len(tool.Annotations) > 0 {
			entry["_meta"] = tool.Annotations
		}
		entries = append(entries, entry)
	}
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp/tools_changed", "params": map[string]any{
		"sessionId": sessionID, "serverName": serverName, "tools": entries,
	}})
}

func (s *Server) NotifyMCPInitialized(sessionID string, toolCount int, elapsedMs uint64) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp_initialized", "params": map[string]any{
		"sessionId": sessionID, "mcpToolCount": toolCount, "elapsedMs": elapsedMs,
	}})
}

func (s *Server) NotifyMCPInitProgress(sessionID string, total, connected int) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp/init_progress", "params": map[string]any{
		"total": total, "connected": connected, "sessionId": sessionID,
	}})
}

func (s *Server) NotifyMCPServersUpdated(sessionID string) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp/servers_updated", "params": map[string]any{
		"mcpServers": mcpServerCatalog(s.lookupSession(sessionID)),
	}})
}

func mcpServerTransportEqual(left, right MCPServer) bool {
	left.DisabledTools = nil
	right.DisabledTools = nil
	return reflect.DeepEqual(left, right)
}

func (s *Server) handleMCP(ctx context.Context, incoming message) {
	var req struct {
		SessionID       string            `json:"sessionId"`
		LegacySessionID string            `json:"session_id"`
		Server          string            `json:"server"`
		ServerName      string            `json:"server_name"`
		ServerNameCamel string            `json:"serverName"`
		ToolName        string            `json:"tool_name"`
		ToolNameCamel   string            `json:"toolName"`
		ServerURL       string            `json:"serverUrl"`
		ServerID        string            `json:"serverId"`
		Message         json.RawMessage   `json:"message"`
		Tool            string            `json:"tool"`
		URI             string            `json:"uri"`
		Arguments       json.RawMessage   `json:"arguments"`
		Enabled         *bool             `json:"enabled"`
		Type            string            `json:"type"`
		Command         string            `json:"command"`
		Args            []string          `json:"args"`
		Env             map[string]string `json:"env"`
		URL             string            `json:"url"`
		Headers         map[string]string `json:"headers"`
		Code            string            `json:"code"`
		CallbackURL     string            `json:"callback_url"`
		CallbackURLCamel string           `json:"callbackUrl"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid MCP parameters")
		return
	}
	if req.SessionID == "" {
		req.SessionID = req.LegacySessionID
	}
	if req.ServerName == "" {
		req.ServerName = req.ServerNameCamel
	}
	if req.ToolName == "" {
		req.ToolName = req.ToolNameCamel
	}
	if incoming.Method == "x.ai/mcp/toggle" || incoming.Method == "x.ai/mcp/toggle_tool" || incoming.Method == "x.ai/mcp/upsert" || incoming.Method == "x.ai/mcp/delete" {
		s.handleMCPConfig(ctx, incoming, req.SessionID, req.ServerName, req.ToolName, req.Enabled, mcppkg.ServerConfig{
			Type: req.Type, Name: req.ServerName, Command: req.Command, Args: req.Args,
			Env: req.Env, URL: req.URL, Headers: req.Headers,
		})
		return
	}
	if incoming.Method == "x.ai/mcp/setup" {
		s.handleMCPSetup(ctx, incoming)
		return
	}
	if incoming.Method == "x.ai/mcp/auth_status" {
		current := s.lookupSession(req.SessionID)
		if current == nil {
			s.respondError(incoming.ID, -32602, "session not found")
			return
		}
		current.mu.Lock()
		configs := append([]MCPServer(nil), current.mcpServers...)
		var provider func() []MCPServer
		if current.runner != nil {
			provider = current.runner.MCPServers
			if provider == nil {
				provider = current.runner.MCPServerCatalog
			}
		}
		current.mu.Unlock()
		if provider != nil {
			configs = provider()
		}
		servers := make([]any, 0)
		for _, config := range configs {
			if mcppkg.NeedsMCPAuth(config, "") {
				servers = append(servers, map[string]any{"server_name": config.Name, "status": "needs_auth"})
			}
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"servers": servers}, "error": nil})
		return
	}
	if incoming.Method == "x.ai/mcp/auth_trigger" {
		current := s.lookupSession(req.SessionID)
		if current == nil || current.runner == nil {
			s.respondError(incoming.ID, -32602, "session not found")
			return
		}
		if req.ServerName == "" {
			s.respondError(incoming.ID, -32602, "server_name is required")
			return
		}
		current.mu.Lock()
		configs := append([]MCPServer(nil), current.mcpServers...)
		provider := current.runner.MCPServers
		if provider == nil {
			provider = current.runner.MCPServerCatalog
		}
		authenticate := current.runner.AuthenticateMCPServer
		current.mu.Unlock()
		if provider != nil {
			configs = provider()
		}
		var target *MCPServer
		for index := range configs {
			if configs[index].Name == req.ServerName {
				target = &configs[index]
				break
			}
		}
		if target == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": "MCP server not found",
			}, "error": nil})
			return
		}
		if strings.TrimSpace(target.URL) == "" {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": "MCP OAuth is not supported for local servers",
			}, "error": nil})
			return
		}
		if authenticate == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": "MCP OAuth enrollment is unavailable",
			}, "error": nil})
			return
		}
		if err := authenticate(ctx, req.ServerName); err != nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": err.Error(),
			}, "error": nil})
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"status": "authenticated", "error": nil,
		}, "error": nil})
		return
	}
	if incoming.Method == "x.ai/mcp/auth_submit" {
		if req.ServerName == "" {
			s.respondError(incoming.ID, -32602, "server_name is required")
			return
		}
		value := strings.TrimSpace(req.CallbackURL)
		if value == "" {
			value = strings.TrimSpace(req.CallbackURLCamel)
		}
		if value == "" {
			value = strings.TrimSpace(req.Code)
		}
		if value == "" {
			s.respondError(incoming.ID, -32602, "callback_url or code is required")
			return
		}
		if err := mcppkg.SubmitMCPAuthCallback(req.ServerName, value); err != nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": err.Error(),
			}, "error": nil})
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"status": "submitted", "error": nil,
		}, "error": nil})
		return
	}
	if incoming.Method == "x.ai/mcp/sdk_message" {
		s.handleMCPSDKMessage(ctx, incoming, req.SessionID, req.ServerID, req.Message)
		return
	}
	if incoming.Method == "x.ai/mcp/list" {
		s.handleMCPList(incoming, req.SessionID)
		return
	}
	if incoming.Method == "x.ai/mcp/read_resource" {
		s.handleMCPReadResource(ctx, incoming, req.SessionID, req.Server, req.URI)
		return
	}
	if req.SessionID == "" || req.Server == "" || req.Tool == "" {
		s.respondError(incoming.ID, -32602, "sessionId, server, and tool are required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	if req.ServerURL != "" {
		current.mu.Lock()
		matched := false
		for _, config := range current.mcpServers {
			if config.Name == req.Server && config.URL == req.ServerURL {
				matched = true
				break
			}
		}
		current.mu.Unlock()
		if !matched {
			s.respondError(incoming.ID, -32000, "MCP server URL not found")
			return
		}
	}
	if len(req.Arguments) == 0 || string(req.Arguments) == "null" {
		req.Arguments = json.RawMessage(`{}`)
	}
	for _, registered := range current.runner.Tools.SnapshotTools() {
		tool, ok := registered.(callableMCPTool)
		if !ok {
			continue
		}
		server, name, _ := tool.MCPIdentity()
		if server != req.Server || name != req.Tool {
			continue
		}
		result, err := tool.CallMCP(ctx, req.Arguments)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		content := make([]map[string]any, 0, len(result.Content))
		for _, block := range result.Content {
			if block.Type == "text" {
				content = append(content, map[string]any{"type": "text", "text": block.Text})
				continue
			}
			encoded, _ := json.Marshal(block)
			content = append(content, map[string]any{"type": block.Type, "text": string(encoded)})
		}
		response := map[string]any{"content": content}
		if result.IsError {
			response["isError"] = true
		}
		s.respond(incoming.ID, map[string]any{"result": response, "error": nil})
		return
	}
	s.respondError(incoming.ID, -32000, "MCP tool not found")
}

// handleMCPSDKMessage delivers one MCP message originated by an in-process
// SDK server to its MCP client. Notifications (no ACP id) are processed
// silently; server-initiated requests return the JSON-RPC response message.
func (s *Server) handleMCPSDKMessage(ctx context.Context, incoming message, sessionID, serverID string, payload json.RawMessage) {
	current := s.lookupSession(sessionID)
	if current == nil || current.runner == nil || current.runner.HandleMCPSDKMessage == nil {
		if len(incoming.ID) > 0 {
			s.respondError(incoming.ID, -32602, "session not found")
		}
		return
	}
	if serverID == "" || len(payload) == 0 {
		if len(incoming.ID) > 0 {
			s.respondError(incoming.ID, -32602, "serverId and message are required")
		}
		return
	}
	response, err := current.runner.HandleMCPSDKMessage(ctx, serverID, payload)
	if len(incoming.ID) == 0 {
		return
	}
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"message": response}, "error": nil})
}

func (s *Server) handleMCPReadResource(ctx context.Context, incoming message, sessionID, server, uri string) {
	if sessionID == "" || server == "" || uri == "" {
		s.respondError(incoming.ID, -32602, "sessionId, server, and uri are required")
		return
	}
	current := s.lookupSession(sessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	for _, registered := range current.runner.Tools.SnapshotTools() {
		reader, ok := registered.(readableMCPResource)
		if !ok {
			continue
		}
		name, readable := reader.MCPResourceReader()
		if name != server || !readable {
			continue
		}
		contents, err := reader.ReadMCPResource(ctx, uri)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		if len(contents) == 0 {
			s.respondError(incoming.ID, -32000, "empty resource")
			return
		}
		result := make([]map[string]any, 0, len(contents))
		for _, content := range contents {
			entry := map[string]any{"uri": content.URI}
			if content.MIMEType != "" {
				entry["mimeType"] = content.MIMEType
			}
			if content.Blob != "" {
				entry["blob"] = content.Blob
			} else {
				entry["text"] = content.Text
			}
			result = append(result, entry)
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"contents": result}, "error": nil})
		return
	}
	s.respondError(incoming.ID, -32000, "MCP server resource reader not found")
}

func (s *Server) handleMCPList(incoming message, sessionID string) {
	servers := mcpServerCatalog(s.lookupSession(sessionID))
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"servers": servers}, "error": nil})
}

func mcpServerCatalog(current *session) []map[string]any {
	if current == nil || current.runner == nil {
		return []map[string]any{}
	}
	current.mu.Lock()
	configs := append([]MCPServer(nil), current.mcpServers...)
	provider := current.runner.MCPServerCatalog
	if provider == nil {
		provider = current.runner.MCPServers
	}
	cwd := current.cwd
	current.mu.Unlock()
	if provider != nil {
		configs = provider()
	}
	toolsByServer := make(map[string][]map[string]any)
	if current.runner.Tools != nil {
		for _, registered := range current.runner.Tools.SnapshotTools() {
			tool, ok := registered.(callableMCPTool)
			if !ok {
				continue
			}
			server, name, info := tool.MCPIdentity()
			entry := map[string]any{"name": name, "enabled": true}
			if info.Title != "" {
				entry["displayName"] = info.Title
			}
			if info.Description != "" {
				entry["description"] = info.Description
			}
			if len(info.Annotations) > 0 {
				entry["_meta"] = info.Annotations
			}
			toolsByServer[server] = append(toolsByServer[server], entry)
		}
	}
	servers := make([]map[string]any, 0, len(configs))
	seen := map[string]bool{}
	for _, config := range configs {
		seen[config.Name] = true
		entry := map[string]any{"name": config.Name, "source": "local"}
		if config.URL != "" {
			entry["type"], entry["url"] = "http", config.URL
		} else {
			entry["type"], entry["command"] = "stdio", config.Command
			if len(config.Args) > 0 {
				entry["args"] = config.Args
			}
			if len(config.Env) > 0 {
				names := make([]string, 0, len(config.Env))
				for name := range config.Env {
					names = append(names, name)
				}
				sort.Strings(names)
				env := make([]map[string]string, 0, len(names))
				for _, name := range names {
					env = append(env, map[string]string{"name": name, "value": config.Env[name]})
				}
				entry["env"] = env
			}
		}
		tools := toolsByServer[config.Name]
		if tools == nil {
			tools = []map[string]any{}
		}
		existing := make(map[string]bool, len(tools))
		for _, tool := range tools {
			existing[tool["name"].(string)] = true
		}
		disabledTools := append([]string(nil), config.DisabledTools...)
		sort.Strings(disabledTools)
		for _, name := range disabledTools {
			if !existing[name] {
				tools = append(tools, map[string]any{"name": name, "enabled": false})
			}
		}
		session := map[string]any{"enabled": !config.Disabled, "tools": tools}
		if !config.Disabled {
			session["status"] = "ready"
		}
		entry["session"] = session
		servers = append(servers, entry)
	}
	servers = append(servers, mcpSetupRequiredPlaceholders(cwd, sessionMCPSetupPlugins(current), seen)...)
	sort.Slice(servers, func(i, j int) bool { return servers[i]["name"].(string) < servers[j]["name"].(string) })
	return servers
}

func sessionMCPSetupPlugins(current *session) []plugin.Plugin {
	if current == nil || current.runner == nil || current.runner.PluginInventory == nil {
		return nil
	}
	inventory := current.runner.PluginInventory()
	out := make([]plugin.Plugin, 0, len(inventory))
	for _, item := range inventory {
		if item.Executable {
			out = append(out, item)
		}
	}
	return out
}

func mcpSetupRequiredPlaceholders(cwd string, plugins []plugin.Plugin, seen map[string]bool) []map[string]any {
	path, err := config.DefaultPath()
	if err != nil {
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil
	}
	trusted := workspace.ResolveFolderTrust(cwd, cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
	entries := config.CollectMCPSetupConfigs(cwd, cfg, plugins, trusted)
	prefs := config.LoadMCPPreferences().File
	out := make([]map[string]any, 0)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if seen[name] {
			continue
		}
		entry := entries[name]
		var pref *config.MCPServerPreferences
		if stored, ok := prefs.Servers[name]; ok {
			copy := stored
			pref = &copy
		}
		resolution := entry.Config.ResolveSetup(pref)
		if resolution.Kind == config.MCPSetupResolved {
			continue
		}
		setup := entry.Config.Setup
		if resolution.Kind == config.MCPSetupRequired || resolution.Kind == config.MCPSetupInvalid {
			setupCopy := resolution.Setup
			if setupCopy.Fields == nil && setup != nil {
				setupCopy = *setup
			}
			setup = &setupCopy
		}
		row := map[string]any{
			"name":   name,
			"source": "local",
			"type":   "http",
			"url":    "",
			"session": map[string]any{
				"enabled":       entry.Config.IsEnabled(),
				"status":        "setuprequired",
				"tools":         []map[string]any{},
				"setupRequired": true,
			},
		}
		if setup != nil {
			row["setup"] = mcpSetupWire(*setup)
		}
		if pref != nil && len(pref.Values) > 0 {
			row["setupValues"] = pref.Values
		}
		if entry.Source.Plugin != nil {
			row["sourceLabel"] = "plugin: " + *entry.Source.Plugin
		}
		out = append(out, row)
	}
	return out
}

func mcpSetupWire(setup config.MCPSetupConfig) map[string]any {
	fields := make([]map[string]any, 0, len(setup.Fields))
	for _, field := range setup.Fields {
		item := map[string]any{
			"id":       field.ID,
			"label":    field.Label,
			"type":     field.Type,
			"required": field.Required,
			"options":  field.Options,
		}
		if field.Default != nil {
			item["default"] = *field.Default
		}
		fields = append(fields, item)
	}
	out := map[string]any{"fields": fields}
	if len(setup.Variables) > 0 {
		out["variables"] = setup.Variables
	}
	return out
}

func (s *Server) handleMCPConfig(ctx context.Context, incoming message, sessionID, name, toolName string, enabled *bool, server mcppkg.ServerConfig) {
	if sessionID == "" || name == "" {
		s.respondError(incoming.ID, -32602, "session_id and server_name are required")
		return
	}
	current := s.lookupSession(sessionID)
	if current == nil || current.runner == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	var err error
	switch incoming.Method {
	case "x.ai/mcp/toggle":
		if enabled == nil || current.runner.ToggleMCPServer == nil {
			s.respondError(incoming.ID, -32602, "enabled is required")
			return
		}
		err = current.runner.ToggleMCPServer(ctx, name, *enabled)
	case "x.ai/mcp/toggle_tool":
		if enabled == nil || toolName == "" {
			s.respondError(incoming.ID, -32602, "tool_name and enabled are required")
			return
		}
		if current.runner.ToggleMCPTool == nil {
			s.respondError(incoming.ID, -32000, "MCP tool configuration is read-only")
			return
		}
		err = current.runner.ToggleMCPTool(ctx, name, toolName, *enabled)
	case "x.ai/mcp/upsert":
		if current.runner.UpsertMCPServer == nil {
			s.respondError(incoming.ID, -32000, "MCP configuration is read-only")
			return
		}
		if enabled != nil && !*enabled {
			s.respondError(incoming.ID, -32602, "server config is disabled")
			return
		}
		if server.URL == "" && server.Command == "" {
			s.respondError(incoming.ID, -32602, "command or url is required")
			return
		}
		if server.Type == "" {
			if server.URL != "" {
				server.Type = "http"
			} else {
				server.Type = "stdio"
			}
		}
		err = current.runner.UpsertMCPServer(ctx, server)
	case "x.ai/mcp/delete":
		if current.runner.DeleteMCPServer == nil {
			s.respondError(incoming.ID, -32000, "MCP configuration is read-only")
			return
		}
		err = current.runner.DeleteMCPServer(ctx, name)
	}
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"ok": true}, "error": nil})
	if incoming.Method == "x.ai/mcp/toggle_tool" {
		s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/mcp/tools_changed", "params": map[string]any{"sessionId": sessionID}})
	}
}
