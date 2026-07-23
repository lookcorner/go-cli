package acp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestReviewCommentCreateAndDeletePersistLocalEvents(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "review-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	current := &session{id: logger.ID(), runner: &agent.Runner{Logger: logger}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleReview(message{ID: json.RawMessage("1"), Method: "x.ai/review/comment", Params: json.RawMessage(`{
		"sessionId":"review-session","promptIndex":2,"comment":"do not persist this",
		"citation":{"path":"main.go","startLine":10,"endLine":12,"text":"quoted code","side":"new"}
	}`)})
	created := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	commentID := created["commentId"].(string)
	if created["recorded"] != true || !isUUIDv7(commentID) {
		t.Fatalf("created=%#v", created)
	}

	output.Reset()
	params, _ := json.Marshal(map[string]any{"sessionId": logger.ID(), "commentId": commentID})
	server.handleReview(message{ID: json.RawMessage("2"), Method: "x.ai/review/comment/delete", Params: params})
	deleted := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if deleted["commentId"] != commentID || deleted["deleted"] != true {
		t.Fatalf("deleted=%#v", deleted)
	}

	events, err := sessionlog.Events(logger.Path(), "review_comment")
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	create := events[0].Data.(map[string]any)
	citation := create["citation"].(map[string]any)
	if create["event"] != "create" || create["commentId"] != commentID || create["promptIndex"] != float64(2) || citation["path"] != "main.go" || citation["startLine"] != float64(10) || citation["endLine"] != float64(12) || citation["text"] != "quoted code" || citation["side"] != "new" {
		t.Fatalf("create=%#v", create)
	}
	if _, stored := create["comment"]; stored {
		t.Fatalf("comment body was persisted: %#v", create)
	}
	remove := events[1].Data.(map[string]any)
	if remove["event"] != "delete" || remove["commentId"] != commentID || remove["sessionId"] != logger.ID() {
		t.Fatalf("delete=%#v", remove)
	}
}

func TestReviewCommentWireDispatch(t *testing.T) {
	root := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID = "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40"
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		logger, err := sessionlog.NewLoggerWithID(sessionDir, cfg.SessionID)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		return &agent.Runner{Tools: registry, Logger: logger}, func() {
			_ = logger.Close()
			_ = registry.Close()
		}, nil
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":` + fmt.Sprintf("%q", root) + `,"_meta":{"sessionId":"` + sessionID + `"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"x.ai/review/comment","params":{"sessionId":"` + sessionID + `","promptIndex":0,"comment":"review","citation":{"path":"main.go","startLine":1,"endLine":1,"text":"line"}}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		if item["id"] == float64(3) && item["result"].(map[string]any)["recorded"] == true {
			return
		}
	}
	t.Fatalf("review response missing: %s", output.String())
}

func TestReviewCommentRejectsInvalidAndPersistenceErrors(t *testing.T) {
	tests := []struct {
		name, method, params, data string
		runner                     *agent.Runner
	}{
		{"malformed create", "x.ai/review/comment", `{`, "invalid review comment parameters", &agent.Runner{}},
		{"missing create field", "x.ai/review/comment", `{"sessionId":"review-errors","promptIndex":0,"comment":"text","citation":{"path":"a","startLine":1,"endLine":1}}`, "invalid review comment parameters", &agent.Runner{}},
		{"negative line", "x.ai/review/comment", `{"sessionId":"review-errors","promptIndex":0,"comment":"text","citation":{"path":"a","startLine":-1,"endLine":1,"text":"x"}}`, "invalid review comment parameters", &agent.Runner{}},
		{"failed create", "x.ai/review/comment", `{"sessionId":"review-errors","promptIndex":0,"comment":"text","citation":{"path":"a","startLine":1,"endLine":1,"text":"x"}}`, "record review comment: read only", &agent.Runner{Logger: reviewErrorLogger{}}},
		{"malformed delete", "x.ai/review/comment/delete", `{`, "invalid review comment delete parameters", &agent.Runner{}},
		{"missing delete field", "x.ai/review/comment/delete", `{"sessionId":"review-errors"}`, "invalid review comment delete parameters", &agent.Runner{}},
		{"failed delete", "x.ai/review/comment/delete", `{"sessionId":"review-errors","commentId":"comment"}`, "delete review comment: read only", &agent.Runner{Logger: reviewErrorLogger{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			sessions := map[string]*session{}
			if test.runner != nil {
				sessions["review-errors"] = &session{id: "review-errors", runner: test.runner}
			}
			server := &Server{output: &output, sessions: sessions}
			server.handleReview(message{ID: json.RawMessage("1"), Method: test.method, Params: json.RawMessage(test.params)})
			errorValue := decodeACPOutput(t, output.Bytes())[0]["error"].(map[string]any)
			if errorValue["data"] != test.data {
				t.Fatalf("error=%#v", errorValue)
			}
		})
	}
}

func TestReviewCommentWithoutResidentSessionMatchesReferenceSuccess(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	server.handleReview(message{ID: json.RawMessage("1"), Method: "x.ai/review/comment", Params: json.RawMessage(`{"sessionId":"dormant","promptIndex":0,"comment":"text","citation":{"path":"a","startLine":1,"endLine":1,"text":"x"}}`)})
	created := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if created["recorded"] != true || !isUUIDv7(created["commentId"].(string)) {
		t.Fatalf("created=%#v", created)
	}
	output.Reset()
	server.handleReview(message{ID: json.RawMessage("2"), Method: "x.ai/review/comment/delete", Params: json.RawMessage(`{"sessionId":"dormant","commentId":"comment"}`)})
	deleted := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if deleted["deleted"] != true || deleted["commentId"] != "comment" {
		t.Fatalf("deleted=%#v", deleted)
	}
}

func TestNewReviewCommentIDIsUUIDv7WithTimestamp(t *testing.T) {
	now := time.UnixMilli(1_725_000_123_456)
	id, err := newUUIDv7(now)
	if err != nil || !isUUIDv7(id) {
		t.Fatalf("id=%q err=%v", id, err)
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil {
		t.Fatal(err)
	}
	milliseconds := uint64(raw[0])<<40 | uint64(raw[1])<<32 | uint64(raw[2])<<24 | uint64(raw[3])<<16 | uint64(raw[4])<<8 | uint64(raw[5])
	if milliseconds != uint64(now.UnixMilli()) {
		t.Fatalf("timestamp=%d want=%d", milliseconds, now.UnixMilli())
	}
}

func isUUIDv7(value string) bool {
	return len(value) == 36 && value[8] == '-' && value[13] == '-' && value[18] == '-' && value[23] == '-' && value[14] == '7' && strings.ContainsRune("89ab", rune(value[19]))
}

type reviewErrorLogger struct{}

func (reviewErrorLogger) Append(string, any) error { return errors.New("read only") }
func (reviewErrorLogger) AppendPrompt(string, []sessionlog.Content) error {
	return errors.New("read only")
}
func (reviewErrorLogger) AppendSyntheticPrompt(string, []sessionlog.Content) error {
	return errors.New("read only")
}
