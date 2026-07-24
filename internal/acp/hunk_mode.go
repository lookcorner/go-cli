package acp

import (
	"encoding/json"
	"strings"

	"github.com/lookcorner/go-cli/internal/tools"
)

func parseClientHunkTrackerMode(raw json.RawMessage) tools.HunkTrackerMode {
	var params struct {
		ClientCapabilities struct {
			Meta struct {
				HunkTracker struct {
					Mode string `json:"mode"`
				} `json:"x.ai/hunkTracker"`
			} `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return tools.HunkTrackerOff
	}
	switch strings.ToLower(strings.TrimSpace(params.ClientCapabilities.Meta.HunkTracker.Mode)) {
	case "":
		return tools.HunkTrackerOff
	case "off", "disabled":
		return tools.HunkTrackerOff
	case "all_dirty":
		return tools.HunkTrackerAllDirty
	case "agent_only":
		return tools.HunkTrackerAgentOnly
	default:
		return tools.HunkTrackerAllDirty
	}
}
