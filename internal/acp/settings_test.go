package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
)

func TestNotifySettingsUpdate(t *testing.T) {
	value, enabled, interval := "value", true, uint64(45)
	remote := &config.RemoteSettings{
		ShowResolvedModel: &enabled, SharingEnabled: &enabled, SessionPickerGrouped: &enabled,
		Tips: []string{"tip"}, Announcements: []config.RemoteAnnouncement{{ID: stringPointer("notice")}},
		GateMessage: &value, GateURL: &value, GateLabel: &value, AllowAccess: &enabled,
		SubscriptionTierDisplay: &value, AutoMode: &config.AutoModeConfig{Enabled: &enabled},
		PermissionMode: &value, GroupToolVerbs: &enabled, CollapsedEditBlocks: &enabled,
		SubscriptionWatchIntervalSeconds: &interval,
	}
	var output bytes.Buffer
	server := &Server{output: &output}
	server.NotifySettingsUpdate(remote)
	notification := decodeACP(t, json.NewDecoder(&output))
	params := notification["params"].(map[string]any)
	if notification["method"] != "x.ai/settings/update" || params["show_resolved_model"] != true || params["sharing_enabled"] != true || params["session_picker_grouped"] != true || params["tips"].([]any)[0] != "tip" || params["announcements"].([]any)[0].(map[string]any)["id"] != "notice" || params["gate_message"] != value || params["gate_url"] != value || params["gate_label"] != value || params["allow_access"] != true || params["subscription_tier_display"] != value || params["auto_permission_mode_enabled"] != true || params["permission_mode"] != value || params["group_tool_verbs"] != true || params["collapsed_edit_blocks"] != true || params["subscription_watch_interval_secs"] != float64(interval) {
		t.Fatalf("notification=%#v", notification)
	}
}

func stringPointer(value string) *string { return &value }

func TestNotifySettingsUpdatePreservesNullFields(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	server.NotifySettingsUpdate(&config.RemoteSettings{})
	params := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)
	for _, field := range []string{"show_resolved_model", "sharing_enabled", "session_picker_grouped", "tips", "announcements", "gate_message", "gate_url", "gate_label", "allow_access", "subscription_tier_display", "auto_permission_mode_enabled", "permission_mode", "group_tool_verbs", "collapsed_edit_blocks", "subscription_watch_interval_secs"} {
		if value, ok := params[field]; !ok || value != nil {
			t.Fatalf("field %q=%#v present=%v", field, value, ok)
		}
	}
	server.NotifySettingsUpdate(nil)
	if output.Len() != 0 {
		t.Fatalf("nil update wrote %q", output.String())
	}
}
