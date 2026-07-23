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
	Methods           []AuthMethod
	DefaultMethodID   string
	TokenProvider     api.TokenProvider
	ProxyBaseURL      string
	HTTP              *http.Client
	Authenticate      func(context.Context, AuthRequest) (*AuthMeta, error)
	GetURL            func(context.Context) (AuthURLResult, error)
	SubmitCode        func(string) error
	CheckSubscription func(context.Context) SubscriptionCheckResult
}

type AuthMethod struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
	Interactive bool           `json:"-"`
}

type AuthRequest struct {
	MethodID string         `json:"methodId"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

type AuthURLResult struct {
	AuthURL          *string `json:"auth_url"`
	ExternalProvider bool    `json:"external_provider"`
	Mode             *string `json:"mode"`
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

func (s *Server) authSnapshot() AuthConfig {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	result := s.Auth
	result.Methods = append([]AuthMethod(nil), s.Auth.Methods...)
	return result
}

// SetAuthState publishes a completed authentication to subsequent ACP calls.
func (s *Server) SetAuthState(methodID, token string) {
	s.authMu.Lock()
	s.Auth.MethodID = methodID
	s.Auth.Token = token
	s.authMu.Unlock()
}

func isSessionAuthMethod(methodID string) bool {
	return methodID == "cached_token" || methodID == "grok.com" || methodID == "oidc"
}

func (s *Server) handleAuthenticate(ctx context.Context, incoming message) {
	var request AuthRequest
	if json.Unmarshal(incoming.Params, &request) != nil || strings.TrimSpace(request.MethodID) == "" {
		s.respondError(incoming.ID, -32602, "methodId is required")
		return
	}
	config := s.authSnapshot()
	if config.Authenticate == nil {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", "authentication is not configured")
		return
	}
	run := func() {
		meta, err := config.Authenticate(ctx, request)
		if err != nil {
			s.respondErrorData(incoming.ID, -32000, "Authentication required", err.Error())
			return
		}
		result := map[string]any{}
		if meta != nil {
			result["_meta"] = meta
		}
		s.respond(incoming.ID, result)
	}
	async := request.MethodID == "cached_token"
	for _, method := range config.Methods {
		if method.ID == request.MethodID && method.Interactive {
			async = true
			break
		}
	}
	if async {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			run()
		}()
		return
	}
	run()
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
	config := s.authSnapshot()
	switch incoming.Method {
	case "x.ai/auth/logout":
		var params struct {
			Scope *string `json:"scope"`
		}
		if len(incoming.Params) == 0 || json.Unmarshal(incoming.Params, &params) != nil || strings.TrimSpace(string(incoming.Params)) == "null" {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid params")
			return
		}
		result, err := auth.Logout(config.Path, config.Scope, params.Scope)
		if err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "failed to logout: "+err.Error())
			return
		}
		if result.ClearedCurrent && isSessionAuthMethod(config.MethodID) {
			s.SetAuthState("", "")
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
		if isSessionAuthMethod(config.MethodID) {
			s.SetAuthState("", "")
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
		if err := auth.StoreAPIKey(config.Path, key); err != nil {
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
		token := config.Token
		if config.MethodID != "xai.api_key" && config.TokenProvider != nil {
			if refreshed, err := config.TokenProvider(ctx, ""); err == nil {
				token = refreshed
			}
		}
		s.respond(incoming.ID, map[string]any{"token": optionalString(token)})
		return
	case "x.ai/auth/get_url":
		result := AuthURLResult{}
		var err error
		if config.GetURL != nil {
			result, err = config.GetURL(ctx)
		}
		if err != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
			return
		}
		s.respond(incoming.ID, result)
		return
	case "x.ai/auth/submit_code":
		var params struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(incoming.Params, &params) != nil || strings.TrimSpace(params.Code) == "" {
			s.respondError(incoming.ID, -32602, "code is required")
			return
		}
		if config.SubmitCode == nil {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", "no pending auth session")
			return
		}
		if err := config.SubmitCode(params.Code); err != nil {
			s.respondErrorData(incoming.ID, -32602, "Invalid params", err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"submitted": true})
		return
	case "x.ai/auth/check_subscription":
		result := SubscriptionCheckResult{}
		if config.CheckSubscription != nil {
			result = config.CheckSubscription(ctx)
		}
		s.respond(incoming.ID, result)
		return
	}

	credential := auth.Credential{}
	methodID := config.MethodID
	if config.Path != "" && config.Scope != "" {
		loaded, err := auth.Load(config.Path, config.Scope)
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
