package announcement

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ptr[T any](value T) *T { return &value }

func TestServiceSelectsCriticalThenPersistsHiddenState(t *testing.T) {
	home := t.TempDir()
	items := []Announcement{
		{ID: ptr("promo"), Message: ptr("upgrade"), Severity: ptr("promo")},
		{ID: ptr("critical"), Message: ptr("outage"), Severity: ptr("critical"), Title: ptr("Incident")},
	}
	service := New(items, home)
	service.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	if current, ok := service.Current(); !ok || value(current.ID) != "critical" || !service.Available() {
		t.Fatalf("current=%#v available=%v", current, service.Available())
	}
	if err := service.Hide(); err != nil {
		t.Fatal(err)
	}
	if current, ok := service.Current(); !ok || value(current.ID) != "promo" {
		t.Fatalf("after hide=%#v ok=%v", current, ok)
	}
	reloaded := New(items, home)
	reloaded.now = service.now
	if current, _ := reloaded.Current(); value(current.ID) != "promo" {
		t.Fatalf("persisted current=%#v", current)
	}
	if err := reloaded.Show(); err != nil {
		t.Fatal(err)
	}
	if current, _ := reloaded.Current(); value(current.ID) != "critical" {
		t.Fatalf("shown current=%#v", current)
	}
	if info, err := os.Stat(filepath.Join(home, "announcements.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file=%#v err=%v", info, err)
	}
}

func TestServiceFiltersAndPinnedAnnouncements(t *testing.T) {
	pinned := false
	service := New([]Announcement{
		{ID: ptr("expired"), Message: ptr("old"), Severity: ptr("critical"), ExpiresAt: ptr("2029-12-31T23:59:59Z")},
		{ID: ptr("info"), Message: ptr("welcome"), Severity: ptr("info")},
		{ID: ptr("pinned"), Message: ptr("required"), Severity: ptr("critical"), Dismissible: &pinned},
	}, t.TempDir())
	service.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	if current, ok := service.Current(); !ok || value(current.ID) != "pinned" {
		t.Fatalf("current=%#v ok=%v", current, ok)
	}
	if err := service.Hide(); err != nil {
		t.Fatal(err)
	}
	if current, ok := service.Current(); !ok || value(current.ID) != "pinned" {
		t.Fatalf("pinned announcement was hidden: %#v ok=%v", current, ok)
	}
}

func TestServiceUsesValidEnvironmentOverride(t *testing.T) {
	t.Setenv("GROK_ANNOUNCEMENTS_OVERRIDE", `[{"id":"override","message":"local","severity":"promo"}]`)
	service := New([]Announcement{{ID: ptr("remote"), Message: ptr("remote"), Severity: ptr("critical")}}, t.TempDir())
	if current, ok := service.Current(); !ok || value(current.ID) != "override" {
		t.Fatalf("override current=%#v ok=%v", current, ok)
	}
	t.Setenv("GROK_ANNOUNCEMENTS_OVERRIDE", "invalid")
	service = New([]Announcement{{ID: ptr("remote"), Message: ptr("remote"), Severity: ptr("critical")}}, t.TempDir())
	if current, ok := service.Current(); !ok || value(current.ID) != "remote" {
		t.Fatalf("fallback current=%#v ok=%v", current, ok)
	}
}

func TestServicePrunesInactiveHiddenIDs(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "announcements.json"), []byte(`{"hidden_ids":["old","live"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New([]Announcement{{ID: ptr("live"), Message: ptr("current"), Severity: ptr("critical")}}, home)
	if _, ok := service.Current(); ok {
		t.Fatal("live hidden announcement unexpectedly visible")
	}
	data, err := os.ReadFile(filepath.Join(home, "announcements.json"))
	if err != nil || string(data) != `{"hidden_ids":["live"]}` {
		t.Fatalf("state=%q err=%v", data, err)
	}
}
