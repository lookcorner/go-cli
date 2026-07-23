package acp

import "github.com/lookcorner/go-cli/internal/config"

type settingsUpdate struct {
	ShowResolvedModel                *bool                       `json:"show_resolved_model"`
	SharingEnabled                   *bool                       `json:"sharing_enabled"`
	SessionPickerGrouped             *bool                       `json:"session_picker_grouped"`
	Tips                             []string                    `json:"tips"`
	Announcements                    []config.RemoteAnnouncement `json:"announcements"`
	GateMessage                      *string                     `json:"gate_message"`
	GateURL                          *string                     `json:"gate_url"`
	GateLabel                        *string                     `json:"gate_label"`
	AllowAccess                      *bool                       `json:"allow_access"`
	SubscriptionTierDisplay          *string                     `json:"subscription_tier_display"`
	AutoPermissionModeEnabled        *bool                       `json:"auto_permission_mode_enabled"`
	PermissionMode                   *string                     `json:"permission_mode"`
	GroupToolVerbs                   *bool                       `json:"group_tool_verbs"`
	CollapsedEditBlocks              *bool                       `json:"collapsed_edit_blocks"`
	SubscriptionWatchIntervalSeconds *uint64                     `json:"subscription_watch_interval_secs"`
}

// NotifySettingsUpdate publishes the latest remote settings snapshot to ACP clients.
func (s *Server) NotifySettingsUpdate(remote *config.RemoteSettings) {
	if remote == nil {
		return
	}
	var autoModeEnabled *bool
	if remote.AutoMode != nil {
		autoModeEnabled = remote.AutoMode.Enabled
	}
	s.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "x.ai/settings/update",
		"params": settingsUpdate{
			ShowResolvedModel: remote.ShowResolvedModel, SharingEnabled: remote.SharingEnabled,
			SessionPickerGrouped: remote.SessionPickerGrouped, Tips: append([]string(nil), remote.Tips...),
			Announcements: append([]config.RemoteAnnouncement(nil), remote.Announcements...), GateMessage: remote.GateMessage,
			GateURL: remote.GateURL, GateLabel: remote.GateLabel, AllowAccess: remote.AllowAccess,
			SubscriptionTierDisplay: remote.SubscriptionTierDisplay, AutoPermissionModeEnabled: autoModeEnabled,
			PermissionMode: remote.PermissionMode, GroupToolVerbs: remote.GroupToolVerbs,
			CollapsedEditBlocks:              remote.CollapsedEditBlocks,
			SubscriptionWatchIntervalSeconds: remote.SubscriptionWatchIntervalSeconds,
		},
	})
}
