package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func StartSSE(ctx context.Context, cfg HTTPConfig) (*Client, InitializeResult, error) {
	endpoint, err := url.Parse(cfg.URL)
	if strings.TrimSpace(cfg.Name) == "" || err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, InitializeResult{}, errors.New("valid MCP SSE server name and URL are required")
	}
	httpClient := cfg.Client
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, InitializeResult{}, err
	}
	request.Header.Set("Accept", "text/event-stream")
	for key, value := range cfg.Headers {
		request.Header.Set(key, value)
	}
	streamResponse, err := httpClient.Do(request)
	if err != nil {
		return nil, InitializeResult{}, fmt.Errorf("connect MCP SSE stream: %w", err)
	}
	if streamResponse.StatusCode < 200 || streamResponse.StatusCode >= 300 {
		defer streamResponse.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(streamResponse.Body, 64<<10))
		return nil, InitializeResult{}, fmt.Errorf("MCP SSE server returned %s: %s", streamResponse.Status, strings.TrimSpace(string(data)))
	}
	if !strings.HasPrefix(strings.ToLower(streamResponse.Header.Get("Content-Type")), "text/event-stream") {
		streamResponse.Body.Close()
		return nil, InitializeResult{}, errors.New("MCP SSE server did not return text/event-stream")
	}
	reader := bufio.NewReader(streamResponse.Body)
	var postURL *url.URL
	for attempts := 0; attempts < 20; attempts++ {
		event, data, readErr := readSSEFrame(reader)
		if readErr != nil {
			streamResponse.Body.Close()
			return nil, InitializeResult{}, fmt.Errorf("read MCP SSE endpoint: %w", readErr)
		}
		if event != "endpoint" {
			continue
		}
		postURL, err = endpoint.Parse(strings.TrimSpace(data))
		if err != nil || postURL.Host != endpoint.Host || postURL.Scheme != endpoint.Scheme {
			streamResponse.Body.Close()
			return nil, InitializeResult{}, errors.New("MCP SSE endpoint must use the configured origin")
		}
		break
	}
	if postURL == nil {
		streamResponse.Body.Close()
		return nil, InitializeResult{}, errors.New("MCP SSE stream did not provide an endpoint")
	}
	client := &Client{
		name: cfg.Name, ssePostURL: postURL.String(), sseStream: streamResponse.Body,
		httpClient: httpClient, headers: cloneHeaders(cfg.Headers),
		pending: make(map[string]chan response), done: make(chan struct{}),
	}
	go client.sseReadLoop(reader)
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var initialized InitializeResult
	if err := client.call(initCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gork-go", "title": "Gork Go", "version": "0.1.0"},
	}, &initialized); err != nil {
		_ = client.Close()
		return nil, InitializeResult{}, fmt.Errorf("initialize MCP SSE server %q: %w", cfg.Name, err)
	}
	if !supportedProtocolVersions[initialized.ProtocolVersion] {
		_ = client.Close()
		return nil, InitializeResult{}, fmt.Errorf("MCP SSE server %q selected unsupported protocol %q", cfg.Name, initialized.ProtocolVersion)
	}
	client.selectedProtocol = initialized.ProtocolVersion
	if err := client.notify("notifications/initialized", nil); err != nil {
		_ = client.Close()
		return nil, InitializeResult{}, err
	}
	return client, initialized, nil
}

func (c *Client) postSSE(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ssePostURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("post MCP SSE message: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("MCP SSE endpoint returned %s", response.Status)
	}
	return nil
}

func (c *Client) sseReadLoop(reader *bufio.Reader) {
	for {
		_, data, err := readSSEFrame(reader)
		if err != nil {
			c.failPending(err)
			return
		}
		var message rpcMessage
		if json.Unmarshal([]byte(data), &message) == nil {
			c.dispatch(message)
		}
	}
}

func readSSEFrame(reader *bufio.Reader) (string, string, error) {
	var event string
	var data []string
	size := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", "", err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		size += len(line)
		if size > 16<<20 {
			return "", "", errors.New("MCP SSE event exceeds 16 MiB")
		}
		if line == "" {
			if len(data) > 0 {
				return event, strings.Join(data, "\n"), nil
			}
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err != nil {
			if len(data) > 0 {
				return event, strings.Join(data, "\n"), nil
			}
			return "", "", err
		}
	}
}
