package acp

import "github.com/lookcorner/go-cli/internal/workspacehub"

func (s *Server) handleWorkspaceHubStatus(incoming message) {
	status := workspacehub.Current()
	s.respond(incoming.ID, map[string]any{
		"enabled": status.Enabled, "workspaceExposure": status.Exposure,
		"hubUrl": status.HubURL, "productUnlocked": status.ProductUnlocked,
		"unlockRequested": status.UnlockRequested, "dialAllowed": status.DialAllowed,
		"blockedReasons": status.BlockedReasons, "unlockRequirements": status.UnlockRequirements,
		"_meta": map[string]any{
			"x.ai/partial": map[string]any{"workspace_hub": true, "reason": "product_blocked"},
		},
	})
}
