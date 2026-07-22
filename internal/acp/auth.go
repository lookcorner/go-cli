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
	Path              string
	Scope             string
	MethodID          string
	Token             string
	TokenProvider     api.TokenProvider
	ProxyBaseURL      string
	HTTP              *http.Client
	CheckSubscription func(context.Context) SubscriptionCheckResult
}

type AuthGate struct {
	Message string  `json:"message"`
	URL     *string `json:"url"`
	Label   *string `json:"label"`
}

type AuthMeta struct {
	Email                     *string   `json:"email"`
	AuthMode                  *string   `json:"auth_mode"`
	TeamID                    *string   `json:"team_id"`
	TeamName                  *string   `json:"team_name"`
	IsZDR                     bool      `json:"is_zdr"`
	TeamRole                  *string   `json:"team_role"`
	CodingDataRetentionOptOut bool      `json:"coding_data_retention_opt_out"`
	ShowResolvedModel         *bool     `json:"show_resolved_model"`
	Gate                      *AuthGate `json:"gate"`
	SubscriptionTier          *string   `json:"subscription_tier"`
}

type SubscriptionCheckResult struct {
	Authenticated bool      `json:"authenticated"`
	Meta          *AuthMeta `json:"meta"`
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
	case "x.ai/auth/logout":
		var params struct {
			Scope *string `json:"scope"`
		}
		if len(incoming.Params) == 0 || json.Unmarshal(incoming.Params, &params) != nil || strings.TrimSpace(string(incoming.Params)) == "null" {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid params")
			return
		}
		result, err := auth.Logout(s.Auth.Path, s.Auth.Scope, params.Scope)
		if err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to logout: "+err.Error())
			return
		}
		if result.ClearedCurrent && s.Auth.MethodID == "cached_token" {
			s.Auth.MethodID = ""
			s.Auth.Token = ""
		}
		if s.AuthChanged != nil {
			if err := s.AuthChanged(ctx, result); err != nil {
				s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to refresh authentication state: "+err.Error())
				return
			}
		}
		s.respond(incoming.ID, map[string]any{
			"ok": true, "was_logged_in": result.WasLoggedIn,
			"email": optionalString(result.Email), "api_key_still_set": result.APIKeyStillSet,
		})
		return
	case "x.ai/internal/auth_cleared":
		if s.Auth.MethodID == "cached_token" {
			s.Auth.MethodID = ""
			s.Auth.Token = ""
		}
		if s.AuthChanged != nil {
			if err := s.AuthChanged(ctx, auth.LogoutResult{ClearedCurrent: true}); err != nil {
				s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to refresh authentication state: "+err.Error())
				return
			}
		}
		s.respond(incoming.ID, map[string]any{"ok": true})
		return
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
			if refreshed, err := s.Auth.TokenProvider(ctx, ""); err == nil {
				token = refreshed
			}
		}
		s.respond(incoming.ID, map[string]any{"token": optionalString(token)})
		return
	case "x.ai/auth/check_subscription":
		result := SubscriptionCheckResult{}
		if s.Auth.CheckSubscription != nil {
			result = s.Auth.CheckSubscription(ctx)
		}
		s.respond(incoming.ID, result)
		return
	}

	credential := auth.Credential{}
	methodID := s.Auth.MethodID
	if s.Auth.Path != "" && s.Auth.Scope != "" {
		loaded, err := auth.Load(s.Auth.Path, s.Auth.Scope)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		if err == nil {
			credential = loaded
		} else if methodID == "cached_token" {
			methodID = ""
		}
	}
	profileImageURL := credential.ProfileImageAssetID
	if profileImageURL != "" && !strings.HasPrefix(profileImageURL, "http://") && !strings.HasPrefix(profileImageURL, "https://") {
		profileImageURL = "grok-asset:///" + profileImageURL
	}
	s.respond(incoming.ID, authInfoResponse{
		MethodID: optionalString(methodID), Email: optionalString(credential.Email),
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
