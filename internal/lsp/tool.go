package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type Manager struct {
	workspace *workspace.Workspace
	clients   map[string]*Client
	mu        sync.RWMutex
	done      chan struct{}
	closed    bool
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewManager(ws *workspace.Workspace) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{workspace: ws, clients: make(map[string]*Client), done: make(chan struct{}), ctx: ctx, cancel: cancel}
}

func (m *Manager) Add(client *Client) error {
	if client == nil {
		return errors.New("LSP client is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("LSP manager is closed")
	}
	if _, exists := m.clients[client.Name()]; exists {
		return fmt.Errorf("LSP server %q already exists", client.Name())
	}
	m.clients[client.Name()] = client
	if client.config.RestartOnCrash && client.config.MaxRestarts > 0 {
		m.wg.Add(1)
		go m.monitor(client.Name(), client)
	}
	return nil
}

func (m *Manager) Replace(clients []*Client) error {
	next := make(map[string]*Client, len(clients))
	for _, client := range clients {
		if client == nil {
			return errors.New("LSP client is nil")
		}
		if _, exists := next[client.Name()]; exists {
			return fmt.Errorf("LSP server %q is duplicated", client.Name())
		}
		next[client.Name()] = client
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("LSP manager is closed")
	}
	previous := m.clients
	m.clients = next
	for name, client := range next {
		if client.config.RestartOnCrash && client.config.MaxRestarts > 0 {
			m.wg.Add(1)
			go m.monitor(name, client)
		}
	}
	m.mu.Unlock()
	for name, client := range previous {
		if next[name] == client {
			continue
		}
		_ = client.Close()
	}
	return nil
}

func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.done)
	m.cancel()
	clients := make(map[string]*Client, len(m.clients))
	for name, client := range m.clients {
		clients[name] = client
	}
	m.mu.Unlock()
	var failures []string
	names := make([]string, 0, len(clients))
	for name := range clients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := clients[name].Close(); err != nil {
			failures = append(failures, name+": "+err.Error())
		}
	}
	m.wg.Wait()
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) Tool() *Tool { return &Tool{manager: m} }

func (m *Manager) client(name string) *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

func (m *Manager) monitor(name string, client *Client) {
	defer m.wg.Done()
	attempts := 0
	backoff := client.config.RestartBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		select {
		case <-m.done:
			return
		case <-client.doneSignal():
		}
		m.mu.RLock()
		closed := m.closed
		current := m.clients[name] == client
		m.mu.RUnlock()
		if closed || !current {
			return
		}
		tracked := client.trackedDocumentURIs()
		_ = client.Close()
		restarted := false
		for attempts < client.config.MaxRestarts {
			select {
			case <-m.done:
				return
			case <-time.After(backoff):
			}
			attempts++
			next, err := Start(m.ctx, client.config)
			if err == nil {
				for _, uri := range tracked {
					path, pathErr := pathFromFileURI(uri)
					if pathErr == nil {
						_, pathErr = next.SyncDocument(path)
					}
					if pathErr != nil {
						err = pathErr
						break
					}
				}
			}
			if err != nil {
				if next != nil {
					_ = next.Close()
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			m.mu.Lock()
			if m.closed || m.clients[name] != client {
				m.mu.Unlock()
				_ = next.Close()
				return
			}
			m.clients[name] = next
			m.mu.Unlock()
			client = next
			backoff = client.config.RestartBackoff
			if backoff <= 0 {
				backoff = time.Second
			}
			restarted = true
			break
		}
		if restarted {
			continue
		}
		m.mu.Lock()
		if m.clients[name] == client {
			delete(m.clients, name)
		}
		m.mu.Unlock()
		return
	}
}

type Tool struct{ manager *Manager }

func (t *Tool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "lsp",
		Description: "Query a configured Language Server for semantic code information or current diagnostics.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "enum": t.manager.Names()},
				"operation": map[string]any{"type": "string", "enum": []string{
					"hover", "definition", "references", "document_symbols", "workspace_symbols", "diagnostics",
				}},
				"path":      map[string]any{"type": "string"},
				"line":      map[string]any{"type": "integer", "minimum": 1},
				"character": map[string]any{"type": "integer", "minimum": 1},
				"query":     map[string]any{"type": "string"},
			},
			"required": []string{"server", "operation"}, "additionalProperties": false,
		},
	}
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Server    string `json:"server"`
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
		Query     string `json:"query"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode LSP arguments: %w", err)
	}
	client := t.manager.client(args.Server)
	if client == nil {
		return "", fmt.Errorf("unknown LSP server %q", args.Server)
	}
	if args.Operation == "workspace_symbols" {
		return requestJSON(ctx, client, "workspace/symbol", map[string]any{"query": args.Query})
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required for %s", args.Operation)
	}
	path, err := t.manager.workspace.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	if !supportsExtension(client.Extensions(), filepath.Ext(path)) {
		return "", fmt.Errorf("LSP server %q is not configured for %s files", args.Server, filepath.Ext(path))
	}
	uri, err := client.SyncDocument(path)
	if err != nil {
		return "", err
	}
	if args.Operation == "diagnostics" {
		rawDiagnostics := client.Diagnostics(uri)
		if len(rawDiagnostics) == 0 {
			return "no diagnostics published", nil
		}
		return prettyJSON(rawDiagnostics)
	}
	textDocument := map[string]any{"uri": uri}
	position := map[string]any{"line": max(args.Line, 1) - 1, "character": max(args.Character, 1) - 1}
	switch args.Operation {
	case "hover":
		return requestJSON(ctx, client, "textDocument/hover", map[string]any{"textDocument": textDocument, "position": position})
	case "definition":
		return requestJSON(ctx, client, "textDocument/definition", map[string]any{"textDocument": textDocument, "position": position})
	case "references":
		return requestJSON(ctx, client, "textDocument/references", map[string]any{
			"textDocument": textDocument, "position": position, "context": map[string]any{"includeDeclaration": true},
		})
	case "document_symbols":
		return requestJSON(ctx, client, "textDocument/documentSymbol", map[string]any{"textDocument": textDocument})
	default:
		return "", fmt.Errorf("unknown LSP operation %q", args.Operation)
	}
}

func requestJSON(ctx context.Context, client *Client, method string, params any) (string, error) {
	raw, err := client.Request(ctx, method, params)
	if err != nil {
		return "", err
	}
	return prettyJSON(raw)
}

func prettyJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "no result", nil
	}
	var output bytes.Buffer
	if err := json.Indent(&output, raw, "", "  "); err != nil {
		return "", err
	}
	if output.Len() > 512<<10 {
		return "", errors.New("LSP result exceeds 512 KiB")
	}
	return output.String(), nil
}

func supportsExtension(extensions []string, extension string) bool {
	if len(extensions) == 0 {
		return true
	}
	extension = strings.ToLower(extension)
	for _, allowed := range extensions {
		if extension == allowed {
			return true
		}
	}
	return false
}
