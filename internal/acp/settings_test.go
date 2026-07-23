package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
	decoder := json.NewDecoder(&output)
	notification := decodeACP(t, decoder)
	params := notification["params"].(map[string]any)
	if notification["method"] != "x.ai/settings/update" || params["show_resolved_model"] != true || params["sharing_enabled"] != true || params["session_picker_grouped"] != true || params["tips"].([]any)[0] != "tip" || params["announcements"].([]any)[0].(map[string]any)["id"] != "notice" || params["gate_message"] != value || params["gate_url"] != value || params["gate_label"] != value || params["allow_access"] != true || params["subscription_tier_display"] != value || params["auto_permission_mode_enabled"] != true || params["permission_mode"] != value || params["group_tool_verbs"] != true || params["collapsed_edit_blocks"] != true || params["subscription_watch_interval_secs"] != float64(interval) {
		t.Fatalf("notification=%#v", notification)
	}
	announcement := decodeACP(t, decoder)
	if announcement["method"] != "x.ai/announcements/update" || announcement["params"].(map[string]any)["announcements"].([]any)[0].(map[string]any)["id"] != "notice" {
		t.Fatalf("announcement=%#v", announcement)
	}
}

func TestAnnouncementsEmitOnChangeAndClear(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	a := config.RemoteAnnouncement{ID: stringPointer("a")}
	var output bytes.Buffer
	server := &Server{output: &output}

	server.emitAnnouncements([]config.RemoteAnnouncement{a}, true, announcementsIfChanged, now)
	first := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)
	firstGen := uint64(first["gen"].(float64))
	if firstGen < uint64(now.Unix()) || len(first["announcements"].([]any)) != 1 {
		t.Fatalf("first=%#v", first)
	}
	server.emitAnnouncements([]config.RemoteAnnouncement{a}, true, announcementsIfChanged, now)
	if output.Len() != 0 {
		t.Fatalf("unchanged update wrote %q", output.String())
	}
	server.emitAnnouncements(nil, true, announcementsIfChanged, now)
	cleared := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)
	if uint64(cleared["gen"].(float64)) <= firstGen || len(cleared["announcements"].([]any)) != 0 {
		t.Fatalf("cleared=%#v", cleared)
	}
}

func TestAnnouncementsSeedAndForceModes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	a := config.RemoteAnnouncement{ID: stringPointer("a")}
	var output bytes.Buffer
	server := &Server{output: &output}

	server.emitAnnouncements(nil, false, announcementsSeed, now)
	if output.Len() != 0 {
		t.Fatalf("empty seed wrote %q", output.String())
	}
	server.emitAnnouncements([]config.RemoteAnnouncement{a}, true, announcementsIfChanged, now)
	_ = decodeACP(t, json.NewDecoder(&output))
	server.emitAnnouncements(nil, false, announcementsSeed, now)
	seeded := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)
	if len(seeded["announcements"].([]any)) != 1 {
		t.Fatalf("seeded=%#v", seeded)
	}
	server.emitAnnouncements(nil, true, announcementsIfChanged, now)
	_ = decodeACP(t, json.NewDecoder(&output))
	server.emitAnnouncements(nil, false, announcementsForce, now)
	forced := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)
	if len(forced["announcements"].([]any)) != 0 {
		t.Fatalf("forced=%#v", forced)
	}
}

func TestAnnouncementsFilterExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	expiredAt := now.Add(-time.Second).Format(time.RFC3339)
	expiresAt := now.Add(time.Second).Format(time.RFC3339)
	malformed := "not-a-time"
	var output bytes.Buffer
	server := &Server{output: &output}

	server.emitAnnouncements([]config.RemoteAnnouncement{{ID: stringPointer("expired"), ExpiresAt: &expiredAt}}, true, announcementsIfChanged, now)
	if output.Len() != 0 {
		t.Fatalf("expired-only update wrote %q", output.String())
	}
	server.emitAnnouncements([]config.RemoteAnnouncement{
		{ID: stringPointer("expiring"), ExpiresAt: &expiresAt},
		{ID: stringPointer("malformed"), ExpiresAt: &malformed},
	}, true, announcementsIfChanged, now)
	active := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)["announcements"].([]any)
	if len(active) != 2 {
		t.Fatalf("active=%#v", active)
	}
	server.emitAnnouncements(nil, false, announcementsIfChanged, now.Add(2*time.Second))
	remaining := decodeACP(t, json.NewDecoder(&output))["params"].(map[string]any)["announcements"].([]any)
	if len(remaining) != 1 || remaining[0].(map[string]any)["id"] != "malformed" {
		t.Fatalf("remaining=%#v", remaining)
	}
	server.emitAnnouncements(nil, false, announcementsIfChanged, now.Add(3*time.Second))
	if output.Len() != 0 {
		t.Fatalf("repeated refresh wrote %q", output.String())
	}
}

func TestAnnouncementsRetryAfterWriteFailure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	output := &failOnceWriter{}
	server := &Server{output: output}
	announcements := []config.RemoteAnnouncement{{ID: stringPointer("retry")}}
	server.emitAnnouncements(announcements, true, announcementsIfChanged, now)
	server.emitAnnouncements(announcements, true, announcementsIfChanged, now)
	message := decodeACP(t, json.NewDecoder(&output.Buffer))
	if message["params"].(map[string]any)["announcements"].([]any)[0].(map[string]any)["id"] != "retry" {
		t.Fatalf("message=%#v", message)
	}
}

type failOnceWriter struct {
	bytes.Buffer
	failed bool
}

func (w *failOnceWriter) Write(data []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("write failed")
	}
	return w.Buffer.Write(data)
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
