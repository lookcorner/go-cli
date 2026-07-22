package acp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
)

type AuthConfig struct {
	Path          string
	Scope         string
	MethodID      string
	Token         string
	TokenProvider api.TokenProvider
	ProxyBaseURL  string
	HTTP          *http.Client
}

type authInfoResponse struct {
	MethodID                  *string  `json:"methodId"`
	Email                     *string  `json:"email"`
	FirstName                 *string  `json:"firstName"`
	LastName                  *string  `json:"lastName"`
	ProfileImageURL           *string  `json:"profileImageUrl"`
	TeamID                    *string  `json:"teamId"`
	TeamName                  *string  `json:"teamName"`
	TeamRole                  *string  `json:"teamRole"`
	OrganizationID            *string  `json:"organizationId"`
	OrganizationName          *string  `json:"organizationName"`
	OrganizationRole          *string  `json:"organizationRole"`
	PrincipalType             *string  `json:"principalType"`
	PrincipalID               *string  `json:"principalId"`
	UserBlockedReason         *string  `json:"userBlockedReason"`
	TeamBlockedReasons        []string `json:"teamBlockedReasons"`
	CodingDataRetentionOptOut bool     `json:"codingDataRetentionOptOut"`
}

func (s *Server) handleAuth(ctx context.Context, incoming message) {
	switch incoming.Method {
	case "x.ai/getApiKey":
		key, ok := auth.ReadAPIKeyEnvironment()
		if !ok {
			s.respond(incoming.ID, map[string]any{"key": nil})
		} else {
			s.respond(incoming.ID, map[string]any{"key": key})
		}
		return
	case "x.ai/setApiKey":
		var params any = map[string]any{}
		if len(incoming.Params) > 0 && json.Unmarshal(incoming.Params, &params) != nil {
			s.respondError(incoming.ID, -32602, "invalid params")
			return
		}
		key := ""
		if fields, ok := params.(map[string]any); ok {
			key, _ = fields["key"].(string)
		}
		if err := auth.StoreAPIKey(s.Auth.Path, key); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		var err error
		if key == "" {
			err = os.Unsetenv("XAI_API_KEY")
		} else {
			err = os.Setenv("XAI_API_KEY", key)
		}
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"ok": true})
		return
	case "x.ai/auth/getBearerToken":
		token := s.Auth.Token
		if s.Auth.TokenProvider != nil {
			if refreshed, err := s.Auth.TokenProvider(ctx, ""); err == nil && refreshed != "" {
				token = refreshed
			}
		}
		s.respond(incoming.ID, map[string]any{"token": optionalString(token)})
		return
	}

	credential := auth.Credential{}
	if s.Auth.Path != "" && s.Auth.Scope != "" {
		loaded, err := auth.Load(s.Auth.Path, s.Auth.Scope)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		if err == nil {
			credential = loaded
		}
	}
	profileImageURL := credential.ProfileImageAssetID
	if profileImageURL != "" && !strings.HasPrefix(profileImageURL, "http://") && !strings.HasPrefix(profileImageURL, "https://") {
		profileImageURL = "grok-asset:///" + profileImageURL
	}
	s.respond(incoming.ID, authInfoResponse{
		MethodID: optionalString(s.Auth.MethodID), Email: optionalString(credential.Email),
		FirstName: optionalString(credential.FirstName), LastName: optionalString(credential.LastName),
		ProfileImageURL: optionalString(profileImageURL), TeamID: optionalString(credential.TeamID),
		TeamName: optionalString(credential.TeamName), TeamRole: optionalString(credential.TeamRole),
		OrganizationID: optionalString(credential.OrganizationID), OrganizationName: optionalString(credential.OrganizationName),
		OrganizationRole: optionalString(credential.OrganizationRole), PrincipalType: optionalString(credential.PrincipalType),
		PrincipalID: optionalString(credential.PrincipalID), UserBlockedReason: optionalString(credential.UserBlockedReason),
		TeamBlockedReasons:        append([]string{}, credential.TeamBlockedReasons...),
		CodingDataRetentionOptOut: credential.CodingDataRetentionOptOut,
	})
}
