package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
)

func TestACPAnnouncementsRefreshInterval(t *testing.T) {
	t.Setenv("GROK_ANNOUNCEMENTS_REFRESH_INTERVAL_SECS", "0")
	if interval := acpAnnouncementsRefreshInterval(); interval != time.Second {
		t.Fatalf("clamped interval=%s", interval)
	}
	t.Setenv("GROK_ANNOUNCEMENTS_REFRESH_INTERVAL_SECS", "invalid")
	if interval := acpAnnouncementsRefreshInterval(); interval != 5*time.Minute {
		t.Fatalf("default interval=%s", interval)
	}
}

func TestWatchACPAnnouncementsAppliesFirstSnapshotThenPublishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var fetches, applies, publishes, refreshes atomic.Int32
	done := make(chan struct{})
	go func() {
		watchACPAnnouncements(ctx, time.Millisecond,
			func(context.Context) *config.RemoteSettings {
				fetches.Add(1)
				return &config.RemoteSettings{Announcements: []config.RemoteAnnouncement{{}}}
			},
			func(*config.RemoteSettings) { applies.Add(1) },
			func([]config.RemoteAnnouncement) {
				if publishes.Add(1) == 1 {
					cancel()
				}
			},
			func() { refreshes.Add(1) },
		)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("announcement watcher did not stop")
	}
	if fetches.Load() < 2 || applies.Load() != 1 || publishes.Load() != 1 || refreshes.Load() < 2 {
		t.Fatalf("fetches=%d applies=%d publishes=%d refreshes=%d", fetches.Load(), applies.Load(), publishes.Load(), refreshes.Load())
	}
}
