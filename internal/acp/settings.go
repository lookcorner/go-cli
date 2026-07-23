package acp

import (
	"reflect"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
)

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

type announcementState struct {
	mu      sync.Mutex
	current []config.RemoteAnnouncement
	last    []config.RemoteAnnouncement
	gen     uint64
}

type announcementMode uint8

const (
	announcementsIfChanged announcementMode = iota
	announcementsSeed
	announcementsForce
)

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
	s.NotifyAnnouncements(remote.Announcements)
}

// NotifyAnnouncements stores a remote snapshot and publishes it when visible state changed.
func (s *Server) NotifyAnnouncements(announcements []config.RemoteAnnouncement) {
	s.emitAnnouncements(announcements, true, announcementsIfChanged, time.Now())
}

// SeedAnnouncements replays a non-empty snapshot for a newly initialized client.
func (s *Server) SeedAnnouncements() {
	s.emitAnnouncements(nil, false, announcementsSeed, time.Now())
}

// RefreshAnnouncements publishes a one-time clearing update when entries expire.
func (s *Server) RefreshAnnouncements() {
	s.emitAnnouncements(nil, false, announcementsIfChanged, time.Now())
}

// ForceAnnouncements republishes the snapshot after a new session is created.
func (s *Server) ForceAnnouncements() {
	s.emitAnnouncements(nil, false, announcementsForce, time.Now())
}

func (s *Server) emitAnnouncements(next []config.RemoteAnnouncement, replace bool, mode announcementMode, now time.Time) {
	state := &s.announcements
	state.mu.Lock()
	defer state.mu.Unlock()
	if replace {
		state.current = append([]config.RemoteAnnouncement(nil), next...)
	}
	visible := activeAnnouncements(state.current, now)
	push := len(visible) != len(state.last) || len(visible) > 0 && !reflect.DeepEqual(visible, state.last)
	if mode == announcementsSeed {
		push = push || len(visible) > 0
	} else if mode == announcementsForce {
		push = true
	}
	if !push {
		return
	}
	nowSeconds := uint64(now.Unix())
	state.gen = max(nowSeconds, state.gen+1)
	if !s.writeResult(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/announcements/update",
		"params": map[string]any{"gen": state.gen, "announcements": visible},
	}) {
		return
	}
	state.last = append([]config.RemoteAnnouncement(nil), visible...)
}

func activeAnnouncements(announcements []config.RemoteAnnouncement, now time.Time) []config.RemoteAnnouncement {
	active := make([]config.RemoteAnnouncement, 0, len(announcements))
	for _, announcement := range announcements {
		if announcement.ExpiresAt != nil {
			if expiresAt, err := time.Parse(time.RFC3339, *announcement.ExpiresAt); err == nil && !expiresAt.After(now) {
				continue
			}
		}
		active = append(active, announcement)
	}
	return active
}
