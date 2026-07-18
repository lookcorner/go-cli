package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
)

const maxWebSearchResponseBytes = 8 << 20

type webSearchTool struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewWebSearchTool(baseURL, apiKey, model string, client *http.Client) Tool {
	return &webSearchTool{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, client: client,
	}
}

func (t *webSearchTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "web_search",
		Description: "Search the web for up-to-date information, tailored for coding and software development tasks.",
		Parameters: objectSchema(map[string]any{
			"query": map[string]any{"type": "string", "description": "The search query to perform."},
			"allowed_domains": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "Optional domains to restrict search to.",
			},
		}, "query"),
	}
}

func (t *webSearchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query          string   `json:"query"`
		AllowedDomains []string `json:"allowed_domains"`
	}
	if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.Query) == "" {
		return "", errors.New("query is required")
	}
	tool := map[string]any{"type": "web_search"}
	if args.AllowedDomains != nil {
		tool["filters"] = map[string]any{"allowed_domains": args.AllowedDomains}
	}
	body, err := json.Marshal(map[string]any{
		"model": t.model, "input": args.Query, "tools": []any{tool}, "store": false,
		"temperature": 0.1, "top_p": 0.95, "max_output_tokens": 8192,
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	apiKey := t.apiKey
	if refreshed := strings.TrimSpace(os.Getenv("GORK_WEB_SEARCH_API_KEY")); refreshed != "" {
		apiKey = refreshed
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "gork-go/0.1")
	client := t.client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("web search request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebSearchResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read web search response: %w", err)
	}
	if len(data) > maxWebSearchResponseBytes {
		return "", fmt.Errorf("web search response exceeds %d bytes", maxWebSearchResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("web search returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("decode web search response: %w", err)
	}
	var content []string
	for _, output := range result.Output {
		if output.Type != "message" {
			continue
		}
		for _, part := range output.Content {
			if part.Type == "output_text" && part.Text != "" {
				content = append(content, part.Text)
			}
		}
	}
	text := strings.Join(content, "")
	if text == "" {
		text = "No search results found."
	}
	return fmt.Sprintf("Web search results for: %q\n\n%s", args.Query, text), nil
}
