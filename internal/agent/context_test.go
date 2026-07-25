package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type contextFixtureTool struct{ definition api.ToolDefinition }

func (t contextFixtureTool) Definition() api.ToolDefinition { return t.definition }
func (t contextFixtureTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

type contextMCPFixtureTool struct {
	contextFixtureTool
	server string
}

func (t contextMCPFixtureTool) MCPServerName() string { return t.server }

func TestContextSnapshotReportsDefinitionsSkillsAndMCPServers(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	definition := api.ToolDefinition{Name: "demo", Description: "Demo tool", Parameters: map[string]any{"type": "object"}}
	if err := registry.Register(contextFixtureTool{definition: definition}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []contextMCPFixtureTool{
		{contextFixtureTool: contextFixtureTool{definition: api.ToolDefinition{Name: "alpha_echo", Description: "Echo", Parameters: map[string]any{"type": "object"}}}, server: "alpha"},
		{contextFixtureTool: contextFixtureTool{definition: api.ToolDefinition{Name: "beta_add", Description: "Add", Parameters: map[string]any{"type": "object"}}}, server: "beta"},
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	skillDir := filepath.Join(root, "skills", "review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review code\n---\nReview it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(root, skills.Config{Paths: []string{filepath.Join(root, "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	logger, err := session.NewLoggerWithID(t.TempDir(), "context-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.AppendPrompt("hello world", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "response-1", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		Tools: registry, Skills: catalog, Instructions: "system prompt", SessionPath: logger.Path(),
		ContextWindow: 1000, CompactThresholdPercent: 80,
	}
	snapshot := runner.ContextSnapshot(250)
	if snapshot.Used != 250 || snapshot.Total != 1000 || snapshot.UsagePct != 25 || snapshot.FreeTokens != 750 || snapshot.AutoCompactThresholdPercent != 80 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if snapshot.SystemPromptTokens != len("system prompt")/4 || snapshot.ToolDefinitionsCount < 1 || snapshot.ToolDefinitionsTokens == 0 || snapshot.MessageCount != 2 || snapshot.MessageTokens != (len("hello world")+len("done"))/4 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if len(snapshot.UsageCategories) != 2 || snapshot.UsageCategories[0].Label != "Skills" || snapshot.UsageCategories[0].Detail != "1 skill" || snapshot.UsageCategories[1].Label != "MCP servers" || snapshot.UsageCategories[1].Detail != "2 servers" {
		t.Fatalf("categories=%#v", snapshot.UsageCategories)
	}
	wantMCPTokens := estimateToolDefinitionTokens(api.ToolDefinition{Name: "alpha_echo", Description: "Echo", Parameters: map[string]any{"type": "object"}}) +
		estimateToolDefinitionTokens(api.ToolDefinition{Name: "beta_add", Description: "Add", Parameters: map[string]any{"type": "object"}})
	if snapshot.UsageCategories[1].Tokens != wantMCPTokens {
		t.Fatalf("MCP tokens=%d want=%d", snapshot.UsageCategories[1].Tokens, wantMCPTokens)
	}
	text := snapshot.Markdown("test-model")
	for _, want := range []string{"Tool definitions:", "Skills:", "MCP servers:", "Auto-compact at 80%", "test-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}
