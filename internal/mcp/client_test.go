package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestStdioLifecycleAndToolCall(t *testing.T) {
	sampled := make(chan SamplingRequest, 1)
	client, initialized, err := Start(context.Background(), ProcessConfig{
		Name:    "fixture",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     map[string]string{"GORK_GO_MCP_HELPER": "1", "GORK_GO_MCP_SAMPLE": "1"},
		Stderr:  io.Discard,
		Sampling: func(_ context.Context, request SamplingRequest) (SamplingResult, error) {
			sampled <- request
			return SamplingResult{
				Role: "assistant", Content: SamplingContent{Type: "text", Text: "sampled answer"},
				Model: "fixture-model", StopReason: "endTurn",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case request := <-sampled:
		if request.SystemPrompt != "Be concise" || request.MaxTokens != 64 || len(request.Messages) != 1 || request.Messages[0].Content.Text != "sample this" {
			t.Fatalf("unexpected sampling request: %#v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP server sampling request was not handled")
	}
	if initialized.ProtocolVersion != protocolVersion || initialized.ServerInfo.Name != "fixture-server" {
		t.Fatalf("unexpected initialize result: %#v", initialized)
	}
	remoteTools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(remoteTools) != 1 || remoteTools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %#v", remoteTools)
	}
	adapters := NewToolAdapters(client, "fixture", remoteTools, tools.PromptApprover{Mode: tools.PermissionAuto})
	output, err := adapters[0].Execute(context.Background(), json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if output != "echo: hello" {
		t.Fatalf("unexpected tool output: %q", output)
	}
	imageResult, err := adapters[0].ExecuteResult(context.Background(), json.RawMessage(`{"message":"image"}`))
	if err != nil || len(imageResult.Images) != 1 || imageResult.Images[0].MediaType != "image/png" || !strings.Contains(imageResult.Output, "[Image: image/png, 1x1]") {
		t.Fatalf("unexpected MCP image result=%#v err=%v", imageResult, err)
	}
	resources := NewResourceAdapters(client, "fixture")
	listed, err := resources[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(listed, "file:///fixture/readme.md") {
		t.Fatalf("unexpected resource list: %q err=%v", listed, err)
	}
	read, err := resources[1].Execute(context.Background(), json.RawMessage(`{"uri":"file:///fixture/readme.md"}`))
	if err != nil || !strings.Contains(read, "fixture resource") {
		t.Fatalf("unexpected resource contents: %q err=%v", read, err)
	}
	updates := make(chan ResourceUpdate, 1)
	client.SetResourceUpdateHandler(func(update ResourceUpdate) { updates <- update })
	if err := client.SubscribeResource(context.Background(), "file:///fixture/readme.md"); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.URI != "file:///fixture/readme.md" {
			t.Fatalf("unexpected resource update: %#v", update)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP resource update was not dispatched")
	}
	if err := client.UnsubscribeResource(context.Background(), "file:///fixture/readme.md"); err != nil {
		t.Fatal(err)
	}
	prompts := NewPromptAdapters(client, "fixture")
	promptList, err := prompts[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(promptList, "review") {
		t.Fatalf("unexpected prompt list: %q err=%v", promptList, err)
	}
	rendered, err := prompts[1].Execute(context.Background(), json.RawMessage(`{"name":"review","arguments":{"focus":"tests"}}`))
	if err != nil || !strings.Contains(rendered, "Review tests") {
		t.Fatalf("unexpected rendered prompt: %q err=%v", rendered, err)
	}
}

func TestModelToolNameIsValidAndBounded(t *testing.T) {
	name := modelToolName(strings.Repeat("server name ", 10), strings.Repeat("tool/name ", 10))
	if len(name) > 64 {
		t.Fatalf("tool name exceeds limit: %d", len(name))
	}
	if invalidToolName.MatchString(name) {
		t.Fatalf("tool name contains invalid characters: %q", name)
	}
}

func TestResourceSubscriptionRequiresServerCapability(t *testing.T) {
	client := &Client{}
	err := client.SubscribeResource(context.Background(), "file:///fixture/readme.md")
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported subscription error=%v", err)
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GORK_GO_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if request.ID == nil {
			if request.Method == "notifications/initialized" && os.Getenv("GORK_GO_MCP_SAMPLE") == "1" {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": 900, "method": "sampling/createMessage",
					"params": map[string]any{
						"systemPrompt": "Be concise", "maxTokens": 64,
						"messages": []any{map[string]any{
							"role": "user", "content": map[string]any{"type": "text", "text": "sample this"},
						}},
					},
				})
			}
			continue
		}
		if request.Method == "" {
			content, _ := request.Result["content"].(map[string]any)
			if request.ID != float64(900) || content["text"] != "sampled answer" || request.Result["model"] != "fixture-model" {
				os.Exit(3)
			}
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			capabilities, _ := request.Params["capabilities"].(map[string]any)
			if os.Getenv("GORK_GO_MCP_SAMPLE") == "1" {
				if _, ok := capabilities["sampling"]; !ok {
					os.Exit(4)
				}
			}
			result = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{}, "resources": map[string]any{"subscribe": true}, "prompts": map[string]any{},
				},
				"serverInfo": map[string]any{"name": "fixture-server", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "Echo a message",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"message": map[string]any{"type": "string"}},
					"required":   []string{"message"},
				},
			}}}
		case "tools/call":
			arguments, _ := request.Params["arguments"].(map[string]any)
			if arguments["message"] == "image" {
				result = map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "fixture image"},
					map[string]any{
						"type": "image", "mimeType": "image/png",
						"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
					},
				}}
			} else {
				result = map[string]any{"content": []any{map[string]any{
					"type": "text", "text": fmt.Sprintf("echo: %v", arguments["message"]),
				}}}
			}
		case "resources/list":
			result = map[string]any{"resources": []any{map[string]any{
				"uri": "file:///fixture/readme.md", "name": "readme", "mimeType": "text/markdown",
			}}}
		case "resources/read":
			result = map[string]any{"contents": []any{map[string]any{
				"uri": request.Params["uri"], "mimeType": "text/markdown", "text": "fixture resource",
			}}}
		case "resources/subscribe":
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}); err != nil {
				os.Exit(3)
			}
			if err := encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "notifications/resources/updated",
				"params": map[string]any{"uri": request.Params["uri"]},
			}); err != nil {
				os.Exit(3)
			}
			continue
		case "resources/unsubscribe":
			result = map[string]any{}
		case "prompts/list":
			result = map[string]any{"prompts": []any{map[string]any{
				"name": "review", "description": "Review code",
				"arguments": []any{map[string]any{"name": "focus", "required": true}},
			}}}
		case "prompts/get":
			arguments, _ := request.Params["arguments"].(map[string]any)
			result = map[string]any{
				"description": "Rendered review",
				"messages": []any{map[string]any{
					"role": "user", "content": map[string]any{"type": "text", "text": fmt.Sprintf("Review %v", arguments["focus"])},
				}},
			}
		default:
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "unknown method"},
			})
			continue
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}
