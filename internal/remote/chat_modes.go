package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

// ErrNoAuth indicates no bearer credential for chat modes.
var ErrNoAuth = errors.New("no grok.com credentials")

// Mode is one grok.com /rest/modes picker entry.
type Mode struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	BadgeText    string           `json:"badgeText"`
	Availability ModeAvailability `json:"availability"`
	IconHint     string           `json:"iconHint"`
	Tags         []string         `json:"tags"`
}

// ModeAvailability is the proto3-JSON oneof for mode availability.
type ModeAvailability struct {
	Available        json.RawMessage `json:"available"`
	Unavailable      json.RawMessage `json:"unavailable"`
	RequiresUpgrade  json.RawMessage `json:"requiresUpgrade"`
	ComingSoon       json.RawMessage `json:"comingSoon"`
}

// IsAvailable reports whether the mode is selectable.
func (m Mode) IsAvailable() bool {
	return len(bytes.TrimSpace(m.Availability.Available)) > 0 &&
		string(bytes.TrimSpace(m.Availability.Available)) != "null"
}

// ListModesResponse is POST /rest/modes.
type ListModesResponse struct {
	Modes         []Mode `json:"modes"`
	DefaultModeID string `json:"defaultModeId"`
}

// ChatModesClient fetches the grok.com chat model catalog.
type ChatModesClient struct {
	HTTP          *http.Client
	BaseURL       string
	AuthPath      string
	AuthScope     string
	TokenProvider api.TokenProvider
}

// ResolveModesBaseURL mirrors Rust ChatModelsClient env precedence.
func ResolveModesBaseURL() string {
	for _, key := range []string{"GROK_MODES_BASE_URL", "GROK_CONVERSATIONS_BASE_URL", "GROK_CODE_WEB_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return defaultConversationsBaseURL
}

// ListModes calls POST /rest/modes. Unlike conversations, any bearer works
// (API-key / cached-token chat users are included).
func (c *ChatModesClient) ListModes(ctx context.Context, locale string) (ListModesResponse, error) {
	credential, err := auth.Load(c.AuthPath, c.AuthScope)
	if err != nil || credential.Key == "" {
		return ListModesResponse{}, ErrNoAuth
	}
	token := credential.Key
	if c.TokenProvider != nil {
		token, err = c.TokenProvider(ctx, "")
		if err != nil || token == "" {
			return ListModesResponse{}, ErrNoAuth
		}
	}
	if strings.TrimSpace(locale) == "" {
		locale = "en"
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = ResolveModesBaseURL()
	}
	payload, err := json.Marshal(map[string]string{"locale": locale})
	if err != nil {
		return ListModesResponse{}, err
	}
	endpoint := base + "/rest/modes"

	request := func(accessToken string) (*http.Response, error) {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
		req.Header.Set("x-userid", credential.UserID)
		req.Header.Set("x-grok-client-version", version.Current)
		req.Header.Set("x-grok-client-identifier", "gork-go")
		req.Header.Set("x-grok-client-mode", "interactive")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		if credential.Email != "" {
			req.Header.Set("x-email", credential.Email)
		}
		client := c.HTTP
		if client == nil {
			client = http.DefaultClient
		}
		return client.Do(req)
	}

	response, err := request(token)
	if err != nil {
		return ListModesResponse{}, err
	}
	if response.StatusCode == http.StatusUnauthorized && c.TokenProvider != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = c.TokenProvider(ctx, token)
		if err != nil || token == "" {
			return ListModesResponse{}, ErrNoAuth
		}
		response, err = request(token)
		if err != nil {
			return ListModesResponse{}, err
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return ListModesResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ListModesResponse{}, HTTPError{Status: response.StatusCode}
	}
	var wire ListModesResponse
	if json.Unmarshal(data, &wire) != nil {
		return ListModesResponse{}, errors.New("failed to parse modes response")
	}
	return wire, nil
}

// ModesModelState maps available modes into ACP session model state fields.
type ModesModelState struct {
	CurrentModelID string
	Available      []ModesModelInfo
}

// ModesModelInfo is one selectable chat mode as a model picker row.
type ModesModelInfo struct {
	ModelID     string
	Name        string
	Description string
	Meta        map[string]any
}

// ToModelState keeps only available modes and reconciles current to default/first.
func (r ListModesResponse) ToModelState() ModesModelState {
	available := make([]ModesModelInfo, 0, len(r.Modes))
	for _, mode := range r.Modes {
		if !mode.IsAvailable() {
			continue
		}
		name := strings.TrimSpace(mode.Title)
		if name == "" {
			name = mode.ID
		}
		meta := map[string]any{}
		if badge := strings.TrimSpace(mode.BadgeText); badge != "" {
			meta["badgeText"] = badge
		}
		if hint := strings.TrimSpace(mode.IconHint); hint != "" {
			meta["iconHint"] = hint
		}
		if len(mode.Tags) > 0 {
			meta["tags"] = append([]string(nil), mode.Tags...)
		}
		if len(meta) == 0 {
			meta = nil
		}
		available = append(available, ModesModelInfo{
			ModelID: mode.ID, Name: name, Description: mode.Description, Meta: meta,
		})
	}
	current := strings.TrimSpace(r.DefaultModeID)
	inSet := false
	for _, item := range available {
		if item.ModelID == current {
			inSet = true
			break
		}
	}
	if !inSet {
		current = ""
		if len(available) > 0 {
			current = available[0].ModelID
		}
	}
	return ModesModelState{CurrentModelID: current, Available: available}
}

// ListModesWithTimeout fetches modes with a short cold-miss budget.
func (c *ChatModesClient) ListModesWithTimeout(ctx context.Context, locale string, timeout time.Duration) (ListModesResponse, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.ListModes(ctx, locale)
}
