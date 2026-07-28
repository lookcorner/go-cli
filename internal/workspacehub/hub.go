// Package workspacehub documents and enforces the product-blocked remote
// workspace hub / relay surface (computer-hub.grok.com).
//
// Go stays fail-closed: workspace_exposure remains false and Dial never opens a
// hub WebSocket until product unlock criteria are met in-tree.
package workspacehub

import (
	"context"
	"errors"
	"os"
	"strings"
)

// DefaultHubURL matches the Rust xai-workspace-server default.
const DefaultHubURL = "wss://computer-hub.grok.com/v1/tools"

// EnvHubURL overrides the advertised hub URL for inspect/status only.
// It does not authorize dialing.
const EnvHubURL = "GROK_WORKSPACE_HUB_URL"

// EnvUnlockRequest is a local developer signal that product unlock is desired.
// Dial still refuses until ProductUnlockGranted is flipped in a release that
// ships OIDC hub auth + preview supervisor parity.
const EnvUnlockRequest = "GROK_WORKSPACE_HUB_UNLOCK"

// ProductUnlockGranted is the in-tree product gate. Keep false until hub auth,
// preview supervisor, and remote workspace tooling are intentionally shipped.
const ProductUnlockGranted = false

// ErrProductBlocked is returned by Dial while the hub remains product-blocked.
var ErrProductBlocked = errors.New("workspace hub dial blocked: product unlock required (OIDC hub auth + preview supervisor)")

// Status is the fail-closed hub posture for inspect / ACP.
type Status struct {
	Enabled            bool     `json:"enabled"`
	Exposure           bool     `json:"workspaceExposure"`
	HubURL             string   `json:"hubUrl"`
	ProductUnlocked    bool     `json:"productUnlocked"`
	UnlockRequested    bool     `json:"unlockRequested"`
	DialAllowed        bool     `json:"dialAllowed"`
	BlockedReasons     []string `json:"blockedReasons"`
	UnlockRequirements []string `json:"unlockRequirements"`
}

// ResolveHubURL returns the advertised hub endpoint (env override or default).
func ResolveHubURL() string {
	if value := strings.TrimSpace(os.Getenv(EnvHubURL)); value != "" {
		return value
	}
	return DefaultHubURL
}

// UnlockRequested reports whether GROK_WORKSPACE_HUB_UNLOCK is set.
func UnlockRequested() bool {
	value := strings.TrimSpace(os.Getenv(EnvUnlockRequest))
	switch strings.ToLower(value) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Current returns the fail-closed status snapshot.
func Current() Status {
	reasons := []string{}
	requirements := []string{
		"product unlock (Workspace server / computer-hub OIDC)",
		"preview supervisor parity",
		"remote workspace tool server wiring",
	}
	if !ProductUnlockGranted {
		reasons = append(reasons, "product unlock not granted in this build")
	}
	if !UnlockRequested() {
		reasons = append(reasons, "GROK_WORKSPACE_HUB_UNLOCK not set")
	}
	dialAllowed := ProductUnlockGranted && UnlockRequested()
	if !dialAllowed && len(reasons) == 0 {
		reasons = append(reasons, "hub dial disabled")
	}
	return Status{
		Enabled:            false,
		Exposure:           false,
		HubURL:             ResolveHubURL(),
		ProductUnlocked:    ProductUnlockGranted,
		UnlockRequested:    UnlockRequested(),
		DialAllowed:        dialAllowed,
		BlockedReasons:     reasons,
		UnlockRequirements: requirements,
	}
}

// Dial refuses hub connections while product-blocked.
// Even when DialAllowed is true this build still returns ErrProductBlocked
// until a follow-up ships the real WebSocket client.
func Dial(ctx context.Context, hubURL string) error {
	_ = ctx
	_ = hubURL
	status := Current()
	if !status.DialAllowed || !ProductUnlockGranted {
		return ErrProductBlocked
	}
	return errors.New("workspace hub client is not implemented in this build")
}
