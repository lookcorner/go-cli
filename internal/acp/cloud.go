package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

type cloudEnvironmentParams struct {
	EnvironmentID  string  `json:"environment_id"`
	SandboxID      string  `json:"sandbox_id"`
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	Repository     *string `json:"repository"`
	DefaultBranch  *string `json:"default_branch"`
	ContainerImage *string `json:"container_image"`
	SetupScript    *string `json:"setup_script"`
}

func (s *Server) handleCloud(ctx context.Context, incoming message) {
	var params cloudEnvironmentParams
	if json.Unmarshal(incoming.Params, &params) != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid cloud parameters")
		return
	}
	method, path, body := http.MethodGet, "/sandbox/environments", map[string]any(nil)
	switch incoming.Method {
	case "x.ai/cloud/env/create":
		method = http.MethodPost
		body = cloudEnvironmentBody(params)
		body["workspaceDirectory"] = "/workspace"
		body["internetEnabled"] = true
		body["domainAllowlistPreset"] = "common"
		body["allowedHttpMethods"] = "all"
	case "x.ai/cloud/env/update":
		if params.EnvironmentID == "" {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "missing environment_id")
			return
		}
		method, path, body = http.MethodPut, "/sandbox/environments/"+params.EnvironmentID, cloudEnvironmentBody(params)
	case "x.ai/cloud/env/delete":
		if params.EnvironmentID == "" {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "missing environment_id")
			return
		}
		method, path = http.MethodDelete, "/sandbox/environments/"+params.EnvironmentID
	case "x.ai/cloud/terminate":
		if params.SandboxID == "" {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "missing sandbox_id")
			return
		}
		method, path = http.MethodDelete, "/sandbox/sessions/"+params.SandboxID
	}

	payload, err := s.cloudRequest(ctx, method, path, body)
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
		return
	}
	switch incoming.Method {
	case "x.ai/cloud/env/list":
		environments, ok := payload["environments"]
		if !ok {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to parse list environments response")
			return
		}
		s.respond(incoming.ID, map[string]any{"environments": environments})
	case "x.ai/cloud/env/create", "x.ai/cloud/env/update":
		environment, ok := payload["environment"]
		if !ok {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to parse environment response")
			return
		}
		s.respond(incoming.ID, map[string]any{"environment": environment})
	default:
		s.respond(incoming.ID, map[string]any{"ok": true})
	}
}

func cloudEnvironmentBody(params cloudEnvironmentParams) map[string]any {
	body := make(map[string]any)
	for key, value := range map[string]*string{
		"name": params.Name, "description": params.Description, "repository": params.Repository,
		"defaultBranch": params.DefaultBranch, "containerImage": params.ContainerImage, "setupScript": params.SetupScript,
	} {
		if value != nil {
			body[key] = *value
		}
	}
	return body
}

func (s *Server) cloudRequest(ctx context.Context, method, path string, body map[string]any) (map[string]any, error) {
	config := s.authSnapshot()
	credential, err := auth.Load(config.Path, config.Scope)
	if err != nil || credential.Key == "" {
		return nil, fmt.Errorf("Authentication required. Run `gork login` to authenticate")
	}
	token := credential.Key
	if config.TokenProvider != nil {
		token, err = config.TokenProvider(ctx, "")
		if err != nil || token == "" {
			return nil, fmt.Errorf("Authentication required. Run `gork login` to authenticate")
		}
	}
	request := func(token string) (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			data, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return nil, marshalErr
			}
			reader = bytes.NewReader(data)
		}
		req, requestErr := http.NewRequestWithContext(ctx, method, strings.TrimRight(config.ProxyBaseURL, "/")+path, reader)
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
		req.Header.Set("x-userid", credential.UserID)
		req.Header.Set("x-grok-client-version", version.Current)
		req.Header.Set("x-grok-client-identifier", "gork-go")
		req.Header.Set("x-grok-client-mode", "interactive")
		if credential.Email != "" {
			req.Header.Set("x-email", credential.Email)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		client := config.HTTP
		if client == nil {
			client = http.DefaultClient
		}
		return client.Do(req)
	}
	response, err := request(token)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized && config.TokenProvider != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = config.TokenProvider(ctx, token)
		if err != nil || token == "" {
			return nil, fmt.Errorf("Authentication required. Run `gork login` to authenticate")
		}
		response, err = request(token)
		if err != nil {
			return nil, err
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("cloud request failed: %d - %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil, errors.New("failed to parse cloud response")
	}
	return payload, nil
}
