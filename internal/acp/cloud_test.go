package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestCloudExtensionsWireContract(t *testing.T) {
	type requestRecord struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var requests []requestRecord
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer cloud-token" || request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-email") != "user@example.com" {
			t.Errorf("missing cloud auth headers: %#v", request.Header)
		}
		record := requestRecord{Method: request.Method, Path: request.URL.Path}
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&record.Body)
		}
		requests = append(requests, record)
		switch {
		case request.Method == http.MethodGet:
			_, _ = writer.Write([]byte(`{"environments":[{"id":"env-1"}]}`))
		case request.Method == http.MethodPost:
			_, _ = writer.Write([]byte(`{"environment":{"id":"env-created"}}`))
		case request.Method == http.MethodPut:
			_, _ = writer.Write([]byte(`{"environment":{"id":"env-1"}}`))
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer upstream.Close()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "cloud-scope", auth.Credential{Key: "cloud-token", UserID: "user-1", Email: "user@example.com", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/cloud/env/list","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/cloud/env/create","params":{"name":"dev","default_branch":"main"}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"x.ai/cloud/env/update","params":{"environment_id":"env-1","description":"updated"}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"x.ai/cloud/env/delete","params":{"environment_id":"env-1"}}` + "\n" +
			`{"jsonrpc":"2.0","id":5,"method":"x.ai/cloud/terminate","params":{"sandbox_id":"sandbox-1"}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Auth: AuthConfig{Path: authPath, Scope: "cloud-scope", ProxyBaseURL: upstream.URL, HTTP: upstream.Client()},
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	list := decodeACP(t, decoder)["result"].(map[string]any)
	created := decodeACP(t, decoder)["result"].(map[string]any)
	updated := decodeACP(t, decoder)["result"].(map[string]any)
	deleted := decodeACP(t, decoder)["result"].(map[string]any)
	terminated := decodeACP(t, decoder)["result"].(map[string]any)
	if list["environments"].([]any)[0].(map[string]any)["id"] != "env-1" || created["environment"].(map[string]any)["id"] != "env-created" || updated["environment"].(map[string]any)["id"] != "env-1" || deleted["ok"] != true || terminated["ok"] != true {
		t.Fatalf("unexpected cloud responses: %#v %#v %#v %#v %#v", list, created, updated, deleted, terminated)
	}
	if len(requests) != 5 || requests[0].Method != http.MethodGet || requests[0].Path != "/sandbox/environments" || requests[1].Method != http.MethodPost || requests[2].Method != http.MethodPut || requests[2].Path != "/sandbox/environments/env-1" || requests[3].Method != http.MethodDelete || requests[4].Path != "/sandbox/sessions/sandbox-1" {
		t.Fatalf("requests=%#v", requests)
	}
	createBody := requests[1].Body
	if createBody["name"] != "dev" || createBody["defaultBranch"] != "main" || createBody["workspaceDirectory"] != "/workspace" || createBody["internetEnabled"] != true || createBody["domainAllowlistPreset"] != "common" || createBody["allowedHttpMethods"] != "all" {
		t.Fatalf("create body=%#v", createBody)
	}
	if updateBody := requests[2].Body; updateBody["description"] != "updated" || updateBody["workspaceDirectory"] != nil {
		t.Fatalf("update body=%#v", updateBody)
	}
}

func TestCloudExtensionsValidateIDs(t *testing.T) {
	for _, method := range []string{"x.ai/cloud/env/update", "x.ai/cloud/env/delete", "x.ai/cloud/terminate"} {
		t.Run(method, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output}
			server.handleCloud(context.Background(), message{ID: json.RawMessage("1"), Method: method, Params: json.RawMessage(`{}`)})
			response := decodeACP(t, json.NewDecoder(&output))
			if response["error"].(map[string]any)["code"] != float64(-32602) {
				t.Fatalf("response=%#v", response)
			}
		})
	}
}
