package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const protocolVersion = "2025-11-25"

var supportedProtocolVersions = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
	"2024-11-05": true,
}

type ProcessConfig struct {
	Name     string
	Command  string
	Args     []string
	Env      map[string]string
	Dir      string
	Stderr   io.Writer
	Sampling SamplingHandler
}

type SamplingHandler func(context.Context, SamplingRequest) (SamplingResult, error)

type SamplingRequest struct {
	Messages     []SamplingMessage `json:"messages"`
	SystemPrompt string            `json:"systemPrompt,omitempty"`
	MaxTokens    int64             `json:"maxTokens"`
	Temperature  float64           `json:"temperature,omitempty"`
}

type SamplingMessage struct {
	Role    string          `json:"role"`
	Content SamplingContent `json:"content"`
}

type SamplingContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type SamplingResult struct {
	Role       string          `json:"role"`
	Content    SamplingContent `json:"content"`
	Model      string          `json:"model"`
	StopReason string          `json:"stopReason,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("MCP error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type response struct {
	result json.RawMessage
	err    error
}

type Client struct {
	name              string
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	httpURL           string
	ssePostURL        string
	sseStream         io.ReadCloser
	httpClient        *http.Client
	headers           map[string]string
	sessionID         string
	selectedProtocol  string
	resourceSubscribe bool
	notification      func(string)
	resourceUpdate    func(ResourceUpdate)
	sampling          SamplingHandler
	pending           map[string]chan response
	nextID            atomic.Uint64
	mu                sync.Mutex
	writeMu           sync.Mutex
	done              chan struct{}
	once              sync.Once
}

type InitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools *struct {
			ListChanged bool `json:"listChanged,omitempty"`
		} `json:"tools,omitempty"`
		Resources *struct {
			Subscribe   bool `json:"subscribe,omitempty"`
			ListChanged bool `json:"listChanged,omitempty"`
		} `json:"resources,omitempty"`
		Prompts *struct {
			ListChanged bool `json:"listChanged,omitempty"`
		} `json:"prompts,omitempty"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions,omitempty"`
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type ToolResult struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
		URI      string `json:"uri,omitempty"`
		Name     string `json:"name,omitempty"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type ResourceInfo struct {
	URI         string         `json:"uri"`
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	MIMEType    string         `json:"mimeType,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type ResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type ResourceUpdate struct {
	URI string `json:"uri"`
}

type PromptInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Arguments   []struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Required    bool   `json:"required,omitempty"`
	} `json:"arguments,omitempty"`
}

type PromptMessage struct {
	Role    string `json:"role"`
	Content struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
		URI      string `json:"uri,omitempty"`
	} `json:"content"`
}

func Start(ctx context.Context, cfg ProcessConfig) (*Client, InitializeResult, error) {
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Command) == "" {
		return nil, InitializeResult{}, errors.New("MCP server name and command are required")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)
	if cfg.Stderr != nil {
		cmd.Stderr = cfg.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, InitializeResult{}, fmt.Errorf("create MCP stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, InitializeResult{}, fmt.Errorf("create MCP stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, InitializeResult{}, fmt.Errorf("start MCP server %q: %w", cfg.Name, err)
	}
	client := &Client{
		name: cfg.Name, cmd: cmd, stdin: stdin,
		sampling: cfg.Sampling,
		pending:  make(map[string]chan response), done: make(chan struct{}),
	}
	go client.readLoop(stdout)

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var initialized InitializeResult
	err = client.call(initCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    clientCapabilities(cfg.Sampling),
		"clientInfo": map[string]any{
			"name": "gork-go", "title": "Gork Go", "version": "0.1.0",
		},
	}, &initialized)
	if err != nil {
		_ = client.Close()
		return nil, InitializeResult{}, fmt.Errorf("initialize MCP server %q: %w", cfg.Name, err)
	}
	if !supportedProtocolVersions[initialized.ProtocolVersion] {
		_ = client.Close()
		return nil, InitializeResult{}, fmt.Errorf("MCP server %q selected unsupported protocol %q", cfg.Name, initialized.ProtocolVersion)
	}
	client.resourceSubscribe = initialized.Capabilities.Resources != nil && initialized.Capabilities.Resources.Subscribe
	if err := client.notify("notifications/initialized", nil); err != nil {
		_ = client.Close()
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var all []ToolInfo
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor,omitempty"`
		}
		if err := c.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
			return all, nil
		}
		cursor = result.NextCursor
	}
	return nil, errors.New("MCP tools/list exceeded 100 pages")
}

func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (ToolResult, error) {
	var result ToolResult
	err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result, err
}

func (c *Client) SetNotificationHandler(handler func(method string)) {
	c.mu.Lock()
	c.notification = handler
	c.mu.Unlock()
}

func (c *Client) SetResourceUpdateHandler(handler func(ResourceUpdate)) {
	c.mu.Lock()
	c.resourceUpdate = handler
	c.mu.Unlock()
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	c.mu.Lock()
	handler := c.notification
	resourceHandler := c.resourceUpdate
	c.mu.Unlock()
	if handler != nil {
		go handler(method)
	}
	if method == "notifications/resources/updated" && resourceHandler != nil {
		var update ResourceUpdate
		if json.Unmarshal(params, &update) == nil && update.URI != "" {
			go resourceHandler(update)
		}
	}
}

func (c *Client) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	var all []ResourceInfo
	err := c.listPages(ctx, "resources/list", func(result json.RawMessage) (string, error) {
		var page struct {
			Resources  []ResourceInfo `json:"resources"`
			NextCursor string         `json:"nextCursor,omitempty"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return "", err
		}
		all = append(all, page.Resources...)
		return page.NextCursor, nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].URI < all[j].URI })
	return all, err
}

func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContents, error) {
	var result struct {
		Contents []ResourceContents `json:"contents"`
	}
	err := c.call(ctx, "resources/read", map[string]any{"uri": uri}, &result)
	return result.Contents, err
}

func (c *Client) SubscribeResource(ctx context.Context, uri string) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("MCP resource URI is required")
	}
	if !c.resourceSubscribe {
		return errors.New("MCP server does not support resource subscriptions")
	}
	return c.call(ctx, "resources/subscribe", map[string]any{"uri": uri}, nil)
}

func (c *Client) UnsubscribeResource(ctx context.Context, uri string) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("MCP resource URI is required")
	}
	if !c.resourceSubscribe {
		return errors.New("MCP server does not support resource subscriptions")
	}
	return c.call(ctx, "resources/unsubscribe", map[string]any{"uri": uri}, nil)
}

func (c *Client) ListPrompts(ctx context.Context) ([]PromptInfo, error) {
	var all []PromptInfo
	err := c.listPages(ctx, "prompts/list", func(result json.RawMessage) (string, error) {
		var page struct {
			Prompts    []PromptInfo `json:"prompts"`
			NextCursor string       `json:"nextCursor,omitempty"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return "", err
		}
		all = append(all, page.Prompts...)
		return page.NextCursor, nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, err
}

func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (string, []PromptMessage, error) {
	var result struct {
		Description string          `json:"description,omitempty"`
		Messages    []PromptMessage `json:"messages"`
	}
	err := c.call(ctx, "prompts/get", map[string]any{"name": name, "arguments": arguments}, &result)
	return result.Description, result.Messages, err
}

func (c *Client) listPages(ctx context.Context, method string, consume func(json.RawMessage) (string, error)) error {
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		if err := c.call(ctx, method, params, &raw); err != nil {
			return err
		}
		next, err := consume(raw)
		if err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
	return fmt.Errorf("MCP %s exceeded 100 pages", method)
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	if c.httpURL != "" {
		message, err := c.httpRequest(ctx, map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		}, true)
		if err != nil {
			return err
		}
		if message.Error != nil {
			return message.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(message.Result, out); err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		return nil
	}
	idKey := fmt.Sprintf("%d", id)
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[idKey] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
	}()
	if err := c.writeJSON(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return err
	}
	select {
	case reply := <-ch:
		if reply.err != nil {
			return reply.err
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(reply.result, out); err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		_ = c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": ctx.Err().Error()})
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("MCP server %q stopped", c.name)
	}
}

func (c *Client) notify(method string, params any) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	if c.httpURL != "" {
		_, err := c.httpRequest(context.Background(), message, false)
		return err
	}
	return c.writeJSON(message)
}

func (c *Client) writeJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	encoded = append(encoded, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.ssePostURL != "" {
		return c.postSSE(encoded)
	}
	if _, err := c.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write MCP message: %w", err)
	}
	return nil
}

func (c *Client) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}
		c.dispatch(message)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.failPending(err)
}

func (c *Client) dispatch(message rpcMessage) {
	if len(message.ID) > 0 && message.Method != "" {
		if message.Method == "sampling/createMessage" && c.sampling != nil {
			go c.handleSampling(message)
			return
		}
		c.respondUnsupported(message)
		return
	}
	if len(message.ID) == 0 {
		if message.Method != "" {
			c.handleNotification(message.Method, message.Params)
		}
		return
	}
	key := strings.Trim(string(message.ID), "\"")
	c.mu.Lock()
	ch := c.pending[key]
	c.mu.Unlock()
	if ch != nil {
		if message.Error != nil {
			ch <- response{err: message.Error}
		} else {
			ch <- response{result: message.Result}
		}
	}
}

func (c *Client) handleSampling(request rpcMessage) {
	var id any
	if json.Unmarshal(request.ID, &id) != nil {
		return
	}
	var params SamplingRequest
	if err := json.Unmarshal(request.Params, &params); err != nil || params.MaxTokens < 1 || len(params.Messages) == 0 {
		_ = c.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "invalid sampling request"},
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := c.sampling(ctx, params)
	if err != nil {
		_ = c.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32000, "message": err.Error()},
		})
		return
	}
	_ = c.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func clientCapabilities(sampling SamplingHandler) map[string]any {
	capabilities := map[string]any{}
	if sampling != nil {
		capabilities["sampling"] = map[string]any{}
	}
	return capabilities
}

func (c *Client) respondUnsupported(request rpcMessage) {
	var id any
	if err := json.Unmarshal(request.ID, &id); err != nil {
		return
	}
	_ = c.writeJSON(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": -32601, "message": "client method not supported"},
	})
}

func (c *Client) failPending(err error) {
	c.once.Do(func() { close(c.done) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.pending {
		select {
		case ch <- response{err: err}:
		default:
		}
	}
}

func (c *Client) Close() error {
	if c.ssePostURL != "" {
		if c.sseStream != nil {
			_ = c.sseStream.Close()
		}
		c.failPending(io.EOF)
		return nil
	}
	if c.httpURL != "" {
		return c.closeHTTP()
	}
	_ = c.stdin.Close()
	wait := make(chan error, 1)
	go func() { wait <- c.cmd.Wait() }()
	select {
	case err := <-wait:
		c.failPending(io.EOF)
		return err
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		err := <-wait
		c.failPending(io.EOF)
		return err
	}
}

func mergeEnv(base []string, overlay map[string]string) []string {
	values := make(map[string]string, len(base)+len(overlay))
	for _, entry := range base {
		if index := strings.IndexByte(entry, '='); index >= 0 {
			values[entry[:index]] = entry[index+1:]
		}
	}
	for key, value := range overlay {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
