package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

const retentionLockedMessage = "Gork Build locks coding data retention to opt-out; opt-in is not available."

func (s *Server) handlePrivacy(ctx context.Context, incoming message) {
	var params struct {
		OptOut *bool `json:"codingDataRetentionOptOut"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.OptOut == nil {
		s.respondError(incoming.ID, -32602, "codingDataRetentionOptOut is required")
		return
	}
	if !*params.OptOut {
		s.respondError(incoming.ID, -32602, retentionLockedMessage)
		return
	}

	credential, err := auth.Load(s.Auth.Path, s.Auth.Scope)
	if err != nil {
		s.respondError(incoming.ID, -32000, "Authentication required. Run `gork login` to re-authenticate.")
		return
	}
	token := credential.Key
	if s.Auth.TokenProvider != nil {
		token, err = s.Auth.TokenProvider(ctx, "")
	}
	if err != nil || token == "" {
		s.respondError(incoming.ID, -32000, "Authentication required. Run `gork login` to re-authenticate.")
		return
	}

	response, err := s.setCodingDataRetention(ctx, token)
	if err != nil {
		s.respondError(incoming.ID, -32000, "HTTP request failed: "+err.Error())
		return
	}
	if response.StatusCode == http.StatusUnauthorized && s.Auth.TokenProvider != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = s.Auth.TokenProvider(ctx, token)
		if err != nil || token == "" {
			s.respondError(incoming.ID, -32000, "Authentication required. Run `gork login` to re-authenticate.")
			return
		}
		response, err = s.setCodingDataRetention(ctx, token)
		if err != nil {
			s.respondError(incoming.ID, -32000, "HTTP request failed: "+err.Error())
			return
		}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.respondError(incoming.ID, -32000, privacyResponseError(response))
		return
	}

	_ = auth.SetCodingDataRetention(s.Auth.Path, s.Auth.Scope, true)
	s.respond(incoming.ID, map[string]any{"codingDataRetentionOptOut": true})
}

func (s *Server) setCodingDataRetention(ctx context.Context, token string) (*http.Response, error) {
	body := strings.NewReader(`{"codingDataRetentionOptOut":true}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(s.Auth.ProxyBaseURL, "/")+"/privacy/coding-data-retention", body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
	request.Header.Set("x-grok-client-version", version.Current)
	request.Header.Set("x-grok-client-mode", "interactive")
	request.Header.Set("Content-Type", "application/json")
	client := s.Auth.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(request)
}

func privacyResponseError(response *http.Response) string {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var payload map[string]any
	if json.Unmarshal(data, &payload) == nil {
		for _, field := range []string{"error", "message"} {
			if value, ok := payload[field].(string); ok && value != "" {
				return value
			}
		}
	}
	return fmt.Sprintf("server returned HTTP %d", response.StatusCode)
}
