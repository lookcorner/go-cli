package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

type ProcessConfig struct {
	Name                  string
	Command               string
	Transport             string
	Args                  []string
	Env                   map[string]string
	Extensions            []string
	InitializationOptions map[string]any
	Settings              map[string]any
	Root                  string
	Stderr                io.Writer
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message) }

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

type documentState struct {
	content string
	version int
}

type Client struct {
	name        string
	root        string
	extensions  []string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	nextID      atomic.Uint64
	mu          sync.Mutex
	pending     map[string]chan response
	documents   map[string]documentState
	diagnostics map[string]json.RawMessage
	settings    map[string]any
	workspace   *workspace.Workspace
	documentMu  sync.Mutex
	writeMu     sync.Mutex
	done        chan struct{}
	once        sync.Once
}

func Start(ctx context.Context, cfg ProcessConfig) (*Client, error) {
	if cfg.Name == "" || cfg.Command == "" || cfg.Root == "" {
		return nil, errors.New("LSP name, command, and root are required")
	}
	ws, err := workspace.Open(cfg.Root)
	if err != nil {
		return nil, err
	}
	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if transport == "" {
		transport = "stdio"
	}
	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stdout io.Reader
	switch transport {
	case "stdio":
		cmd = exec.CommandContext(ctx, cfg.Command, cfg.Args...)
		cmd.Dir = ws.Root()
		cmd.Env = mergeEnv(os.Environ(), cfg.Env)
		cmd.Stderr = cfg.Stderr
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("create LSP stdin: %w", err)
		}
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("create LSP stdout: %w", err)
		}
		if err = cmd.Start(); err != nil {
			return nil, fmt.Errorf("start LSP server %q: %w", cfg.Name, err)
		}
	case "socket":
		dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		connection, dialErr := (&net.Dialer{}).DialContext(dialCtx, "tcp", cfg.Command)
		cancel()
		if dialErr != nil {
			return nil, fmt.Errorf("connect LSP server %q at %q: %w", cfg.Name, cfg.Command, dialErr)
		}
		stdin, stdout = connection, connection
	default:
		return nil, fmt.Errorf("LSP server %q has unsupported transport %q", cfg.Name, cfg.Transport)
	}
	client := &Client{
		name: cfg.Name, root: ws.Root(), extensions: normalizeExtensions(cfg.Extensions),
		cmd: cmd, stdin: stdin, pending: make(map[string]chan response),
		documents: make(map[string]documentState), diagnostics: make(map[string]json.RawMessage),
		settings: cfg.Settings, workspace: ws,
		done: make(chan struct{}),
	}
	go client.readLoop(stdout)
	rootURI := fileURI(ws.Root())
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var initialized any
	initializeParams := map[string]any{
		"processId": os.Getpid(), "rootUri": rootURI,
		"workspaceFolders": []any{map[string]any{"uri": rootURI, "name": filepath.Base(cfg.Root)}},
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"workspaceFolders": true, "configuration": true,
				"workspaceEdit": map[string]any{
					"documentChanges": true, "failureHandling": "abort",
					"resourceOperations": []string{"create", "rename", "delete"},
				},
			},
			"textDocument": map[string]any{
				"hover": map[string]any{}, "definition": map[string]any{},
				"references": map[string]any{}, "documentSymbol": map[string]any{},
				"publishDiagnostics": map[string]any{"relatedInformation": true},
			},
		},
		"clientInfo": map[string]any{"name": "gork-go", "version": "0.1.0"},
	}
	if cfg.InitializationOptions != nil {
		initializeParams["initializationOptions"] = cfg.InitializationOptions
	}
	err = client.call(initCtx, "initialize", initializeParams, &initialized)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize LSP server %q: %w", cfg.Name, err)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	if cfg.Settings != nil {
		if err := client.notify("workspace/didChangeConfiguration", map[string]any{"settings": cfg.Settings}); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

func (c *Client) Name() string         { return c.name }
func (c *Client) Extensions() []string { return append([]string(nil), c.extensions...) }

func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var result json.RawMessage
	if err := c.call(ctx, method, params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) SyncDocument(path string) (string, error) {
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read LSP document: %w", err)
	}
	if len(data) > 4<<20 {
		return "", errors.New("LSP document exceeds 4 MiB")
	}
	content := string(data)
	uri := fileURI(abs)
	c.mu.Lock()
	state, exists := c.documents[uri]
	changed := false
	if !exists {
		state = documentState{content: content, version: 1}
		c.documents[uri] = state
	} else if state.content != content {
		changed = true
		state.content = content
		state.version++
		c.documents[uri] = state
	}
	c.mu.Unlock()
	if !exists {
		err = c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": languageID(abs), "version": state.version, "text": content,
			},
		})
	} else if changed {
		err = c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": state.version},
			"contentChanges": []any{map[string]any{"text": content}},
		})
	}
	return uri, err
}

func (c *Client) Diagnostics(uri string) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(json.RawMessage(nil), c.diagnostics[uri]...)
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	key := strconv.FormatUint(id, 10)
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()
	if err := c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case reply := <-ch:
		if reply.err != nil {
			return reply.err
		}
		if rawTarget, ok := out.(*json.RawMessage); ok {
			*rawTarget = append((*rawTarget)[:0], reply.result...)
			return nil
		}
		if out != nil {
			return json.Unmarshal(reply.result, out)
		}
		return nil
	case <-ctx.Done():
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("LSP server %q stopped", c.name)
	}
}

func (c *Client) notify(method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) writeMessage(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(header); err != nil {
		return fmt.Errorf("write LSP header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("write LSP body: %w", err)
	}
	return nil
}

func (c *Client) readLoop(reader io.Reader) {
	buffered := bufio.NewReader(reader)
	for {
		length, err := readContentLength(buffered)
		if err != nil {
			c.failPending(err)
			return
		}
		if length < 0 || length > 32<<20 {
			c.failPending(fmt.Errorf("invalid LSP content length %d", length))
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(buffered, body); err != nil {
			c.failPending(err)
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(body, &message); err != nil {
			continue
		}
		if len(message.ID) > 0 && message.Method != "" {
			c.handleServerRequest(message)
			continue
		}
		if message.Method == "textDocument/publishDiagnostics" {
			var params struct {
				URI         string          `json:"uri"`
				Diagnostics json.RawMessage `json:"diagnostics"`
			}
			if json.Unmarshal(message.Params, &params) == nil {
				c.mu.Lock()
				c.diagnostics[params.URI] = append(json.RawMessage(nil), params.Diagnostics...)
				c.mu.Unlock()
			}
			continue
		}
		if len(message.ID) == 0 {
			continue
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
}

func (c *Client) handleServerRequest(request rpcMessage) {
	var id any
	if json.Unmarshal(request.ID, &id) != nil {
		return
	}
	var result any
	switch request.Method {
	case "workspace/configuration":
		var params struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		_ = json.Unmarshal(request.Params, &params)
		values := make([]any, len(params.Items))
		for index, item := range params.Items {
			values[index] = configurationValue(c.settings, item.Section)
		}
		result = values
	case "workspace/workspaceFolders":
		result = []any{map[string]any{"uri": fileURI(c.root), "name": filepath.Base(c.root)}}
	case "client/registerCapability", "client/unregisterCapability":
		result = nil
	case "workspace/applyEdit":
		result = c.applyWorkspaceEdit(request.Params)
	default:
		_ = c.writeMessage(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32601, "message": "client method not supported"},
		})
		return
	}
	_ = c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func configurationValue(settings map[string]any, section string) any {
	if section == "" {
		return settings
	}
	if value, ok := settings[section]; ok {
		return value
	}
	var value any = settings
	for _, key := range strings.Split(section, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value, ok = object[key]
		if !ok {
			return nil
		}
	}
	return value
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = c.call(shutdownCtx, "shutdown", nil, nil)
	cancel()
	_ = c.notify("exit", nil)
	_ = c.stdin.Close()
	if c.cmd == nil {
		c.failPending(io.EOF)
		return nil
	}
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

func readContentLength(reader *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return 0, err
			}
		}
	}
	if length < 0 {
		return 0, errors.New("LSP message missing Content-Length")
	}
	return length, nil
}

func fileURI(path string) string {
	abs, _ := filepath.Abs(path)
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func languageID(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	switch ext {
	case "js", "jsx":
		return "javascript"
	case "ts", "tsx":
		return "typescript"
	case "py":
		return "python"
	case "rs":
		return "rust"
	default:
		return ext
	}
}

func normalizeExtensions(extensions []string) []string {
	result := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if extension != "" && !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		if extension != "" {
			result = append(result, extension)
		}
	}
	sort.Strings(result)
	return result
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
