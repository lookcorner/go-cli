package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestRolloutSurveyExtension(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x.ai/rollout/survey","params":{"sessionId":"survey-session","preferences":["faster-worktrees"],"feedback":"works well"}}` + "\n")
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	response := decodeACP(t, json.NewDecoder(&output))
	if response["result"].(map[string]any)["success"] != true {
		t.Fatalf("response=%#v", response)
	}
}

func TestRolloutSurveyRequiresReferenceFields(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.handleRolloutSurvey(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"survey-session","preferences":[]}`)})
	response := decodeACP(t, json.NewDecoder(&output))
	errorPayload := response["error"].(map[string]any)
	if errorPayload["code"] != float64(-32602) {
		t.Fatalf("response=%#v", response)
	}
}
