package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestSkillRefreshUpdatesAllSessions(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	catalogs := make([]*skills.Catalog, 0, len(roots))
	sessions := make(map[string]*session, len(roots))
	for index, root := range roots {
		catalog, err := skills.Discover(root, skills.Config{})
		if err != nil {
			t.Fatal(err)
		}
		catalogs = append(catalogs, catalog)
		sessionID := string(rune('a' + index))
		sessions[sessionID] = &session{id: sessionID, runner: &agent.Runner{Skills: catalog}}
		path := filepath.Join(root, ".grok", "skills", "skill-"+sessionID, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: skill-"+sessionID+"\ndescription: Added after startup\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	brokenRoot := t.TempDir()
	broken, err := skills.Discover(brokenRoot, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	brokenPath := filepath.Join(brokenRoot, ".grok", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(brokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenPath, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	sessions["broken"] = &session{id: "broken", runner: &agent.Runner{Skills: broken}}

	var output bytes.Buffer
	server := &Server{output: &output, sessions: sessions}
	server.handleSkills(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/skills/refresh-baseline"})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["ok"] != true {
		t.Fatalf("response=%#v", messages)
	}
	for index, catalog := range catalogs {
		items := catalog.List()
		if len(items) != 1 || items[0].Name != "skill-"+string(rune('a'+index)) {
			t.Fatalf("catalog %d=%#v", index, items)
		}
	}

	for _, root := range roots {
		if err := os.RemoveAll(filepath.Join(root, ".grok", "skills")); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	server.handleSkills(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/internal/reload_skills"})
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["reloaded"] != float64(3) {
		t.Fatalf("response=%#v", messages)
	}
	for index, catalog := range catalogs {
		if items := catalog.List(); len(items) != 0 {
			t.Fatalf("catalog %d=%#v", index, items)
		}
	}
}

func TestSkillRefreshRoutesWithoutSessions(t *testing.T) {
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		t.Fatal("skill refresh started a session")
		return nil, nil, nil
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/skills/refresh-baseline","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/internal/reload_skills","params":{}}` + "\n",
	)
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["result"].(map[string]any)["ok"] != true || messages[1]["result"].(map[string]any)["reloaded"] != float64(0) {
		t.Fatalf("messages=%#v", messages)
	}
}
