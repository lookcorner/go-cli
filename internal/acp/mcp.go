package acp

import (
	"context"
	"encoding/json"
	"sort"

	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
)

type callableMCPTool interface {
	MCPIdentity() (string, string, mcppkg.ToolInfo)
	CallMCP(context.Context, json.RawMessage) (mcppkg.ToolResult, error)
}

type readableMCPResource interface {
	MCPResourceReader() (string, bool)
	ReadMCPResource(context.Context, string) ([]mcppkg.ResourceContents, error)
}

func (s *Server) handleMCP(ctx context.Context, incoming message) {
	var req struct {
		SessionID       string          `json:"sessionId"`
		LegacySessionID string          `json:"session_id"`
		Server          string          `json:"server"`
		ServerName      string          `json:"server_name"`
		ServerURL       string          `json:"serverUrl"`
		Tool            string          `json:"tool"`
		URI             string          `json:"uri"`
		Arguments       json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid MCP parameters")
		return
	}
	if req.SessionID == "" {
		req.SessionID = req.LegacySessionID
	}
	if incoming.Method == "x.ai/mcp/auth_status" {
		if s.lookupSession(req.SessionID) == nil {
			s.respondError(incoming.ID, -32602, "session not found")
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"servers": []any{}}, "error": nil})
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
		current.mu.Unlock()
		if provider != nil {
			configs = provider()
		}
		found := false
		for _, config := range configs {
			if config.Name == req.ServerName {
				found = true
				break
			}
		}
		if !found {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"status": "failed", "error": "MCP server not found",
			}, "error": nil})
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"status": "failed", "error": "MCP OAuth is not supported for local servers",
		}, "error": nil})
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
	current := s.lookupSession(sessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"servers": []any{}}, "error": nil})
		return
	}
	current.mu.Lock()
	configs := append([]MCPServer(nil), current.mcpServers...)
	provider := current.runner.MCPServers
	current.mu.Unlock()
	if provider != nil {
		configs = provider()
	}
	toolsByServer := make(map[string][]map[string]any)
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
	servers := make([]map[string]any, 0, len(configs))
	for _, config := range configs {
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
		entry["session"] = map[string]any{"enabled": true, "status": "ready", "tools": tools}
		servers = append(servers, entry)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i]["name"].(string) < servers[j]["name"].(string) })
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"servers": servers}, "error": nil})
}
