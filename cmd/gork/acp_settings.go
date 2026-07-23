package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
)

func acpAnnouncementsRefreshInterval() time.Duration {
	if seconds, err := strconv.ParseUint(os.Getenv("GROK_ANNOUNCEMENTS_REFRESH_INTERVAL_SECS"), 10, 64); err == nil {
		return time.Duration(max(uint64(1), seconds)) * time.Second
	}
	return 5 * time.Minute
}

func watchACPAnnouncements(
	ctx context.Context,
	interval time.Duration,
	fetch func(context.Context) *config.RemoteSettings,
	apply func(*config.RemoteSettings),
	publish func([]config.RemoteAnnouncement),
	refresh func(),
) {
	first := true
	update := func() {
		remote := fetch(ctx)
		if remote != nil {
			if first {
				apply(remote)
				first = false
			} else {
				publish(remote.Announcements)
			}
		}
		refresh()
	}
	update()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}
