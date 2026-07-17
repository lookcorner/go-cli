package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPConfig struct {
	Name    string
	URL     string
	Headers map[string]string
	Client  *http.Client
	// Sampling is used by StartSSE. Streamable HTTP has no reverse channel yet.
	Sampling SamplingHandler
}

func StartHTTP(ctx context.Context, cfg HTTPConfig) (*Client, InitializeResult, error) {
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.URL) == "" {
		return nil, InitializeResult{}, errors.New("MCP HTTP server name and URL are required")
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, InitializeResult{}, fmt.Errorf("invalid MCP HTTP URL %q", cfg.URL)
	}
	httpClient := cfg.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	client := &Client{
		name: cfg.Name, httpURL: cfg.URL, httpClient: httpClient,
		headers: cloneHeaders(cfg.Headers), pending: make(map[string]chan response), done: make(chan struct{}),
	}
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var initialized InitializeResult
	err = client.call(initCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name": "gork-go", "title": "Gork Go", "version": "0.1.0",
		},
	}, &initialized)
	if err != nil {
		return nil, InitializeResult{}, fmt.Errorf("initialize MCP HTTP server %q: %w", cfg.Name, err)
	}
	if !supportedProtocolVersions[initialized.ProtocolVersion] {
		_ = client.Close()
		return nil, InitializeResult{}, fmt.Errorf("MCP HTTP server %q selected unsupported protocol %q", cfg.Name, initialized.ProtocolVersion)
	}
	client.mu.Lock()
	client.selectedProtocol = initialized.ProtocolVersion
	client.mu.Unlock()
	if err := client.notify("notifications/initialized", nil); err != nil {
		_ = client.Close()
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (c *Client) httpRequest(ctx context.Context, value any, expectResponse bool) (rpcMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return rpcMessage{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.httpURL, bytes.NewReader(body))
	if err != nil {
		return rpcMessage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sessionID := c.sessionID
	selectedProtocol := c.selectedProtocol
	c.mu.Unlock()
	for key, headerValue := range c.headers {
		request.Header.Set(key, headerValue)
	}
	if selectedProtocol != "" {
		request.Header.Set("MCP-Protocol-Version", selectedProtocol)
	}
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return rpcMessage{}, fmt.Errorf("MCP HTTP request: %w", err)
	}
	defer response.Body.Close()
	if assigned := response.Header.Get("Mcp-Session-Id"); assigned != "" {
		c.mu.Lock()
		c.sessionID = assigned
		c.mu.Unlock()
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		if expectResponse {
			return rpcMessage{}, fmt.Errorf("MCP HTTP server returned %s without a response", response.Status)
		}
		return rpcMessage{}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return rpcMessage{}, fmt.Errorf("MCP HTTP server returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if !expectResponse {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return rpcMessage{}, nil
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return readMCPEventStream(response.Body, c.handleNotification)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil {
		return rpcMessage{}, err
	}
	if len(data) > 16<<20 {
		return rpcMessage{}, errors.New("MCP HTTP response exceeds 16 MiB")
	}
	var message rpcMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return rpcMessage{}, fmt.Errorf("decode MCP HTTP response: %w", err)
	}
	return message, nil
}

func readMCPEventStream(reader io.Reader, onNotification func(string)) (rpcMessage, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 16<<20+1))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			var message rpcMessage
			if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &message); err == nil {
				if len(message.ID) > 0 {
					return message, nil
				}
				if message.Method != "" && onNotification != nil {
					onNotification(message.Method)
				}
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcMessage{}, err
	}
	if len(dataLines) > 0 {
		var message rpcMessage
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &message); err == nil {
			if len(message.ID) > 0 {
				return message, nil
			}
			if message.Method != "" && onNotification != nil {
				onNotification(message.Method)
			}
		}
	}
	return rpcMessage{}, errors.New("MCP event stream ended without a response")
}

func (c *Client) closeHTTP() error {
	c.mu.Lock()
	sessionID := c.sessionID
	selectedProtocol := c.selectedProtocol
	c.mu.Unlock()
	if sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.httpURL, nil)
	if err != nil {
		return err
	}
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	if selectedProtocol != "" {
		request.Header.Set("MCP-Protocol-Version", selectedProtocol)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("close MCP HTTP session: %s", response.Status)
	}
	return nil
}

func cloneHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		result[key] = value
	}
	return result
}
