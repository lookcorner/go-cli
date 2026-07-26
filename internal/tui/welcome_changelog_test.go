package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/announcement"
)

func TestWelcomeChangelogEventAppliesOnlyToBlankStartup(t *testing.T) {
	for _, test := range []struct {
		name string
		m    *model
		want bool
	}{
		{name: "blank", m: &model{welcomeReady: true}, want: true},
		{name: "not ready", m: &model{}},
		{name: "running", m: &model{welcomeReady: true, running: true}},
		{name: "transcript", m: func() *model {
			m := &model{welcomeReady: true}
			m.transcript.WriteString("started")
			return m
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, _ := test.m.Update(welcomeChangelogEvent{bullets: []string{"Added sessions"}})
			if got := len(updated.(*model).welcomeChangelog) > 0; got != test.want {
				t.Fatalf("installed=%v", got)
			}
		})
	}
}

func TestWelcomeChangelogBannerFitsAndYieldsToAnnouncement(t *testing.T) {
	m := &model{width: 24, height: 16, welcomeReady: true, welcomeChangelog: []string{"Added \x1b[31mvery long session discovery"}}
	banner := m.welcomeChangelogBanner(24)
	if len(banner) != 2 || m.bannerHeight() != 2 {
		t.Fatalf("banner=%q height=%d", banner, m.bannerHeight())
	}
	for _, line := range banner {
		plain := stripUIANSI(line)
		if strings.Contains(plain, "\x1b") || displayWidth(plain) > 24 {
			t.Fatalf("line=%q width=%d", plain, displayWidth(plain))
		}
	}

	text := func(value string) *string { return &value }
	m.runner = &agent.Runner{Announcements: announcement.New([]announcement.Announcement{{
		ID: text("notice"), Title: text("Service notice"), Message: text("Maintenance tonight"), Severity: text("critical"),
	}}, t.TempDir())}
	if height := m.bannerHeight(); height != 2 {
		t.Fatalf("announcement height=%d", height)
	}
	content := stripUIANSI(m.View().Content)
	if !strings.Contains(content, "Service notice") || strings.Contains(content, "What's new") {
		t.Fatalf("content=%q", content)
	}
}

func TestStartupTipFitsAndPrecedesChangelog(t *testing.T) {
	m := &model{
		width: 24, height: 16, welcomeReady: true,
		startupTip:       "Use [31m/settings to control very long startup tips",
		welcomeChangelog: []string{"Added sessions"},
	}
	banner := m.startupTipBanner(24)
	if len(banner) != 1 || m.bannerHeight() != 1 {
		t.Fatalf("banner=%q height=%d", banner, m.bannerHeight())
	}
	plain := stripUIANSI(banner[0])
	if strings.Contains(plain, "\x1b") || displayWidth(plain) > 24 || !strings.HasPrefix(plain, "Tip: ") {
		t.Fatalf("banner=%q width=%d", plain, displayWidth(plain))
	}
	content := stripUIANSI(m.View().Content)
	if !strings.Contains(content, "Tip:") || strings.Contains(content, "What's new") {
		t.Fatalf("content=%q", content)
	}
}

func TestWelcomeChangelogInitFetchesOnlyForBlankStartup(t *testing.T) {
	calls := 0
	m := &model{
		ctx: context.Background(), welcomeReady: true,
		runner: &agent.Runner{FetchChangelog: func(context.Context) []string {
			calls++
			return []string{"Added sessions"}
		}},
	}
	command := m.welcomeChangelogCmd()
	if command == nil {
		t.Fatal("blank startup did not schedule changelog fetch")
	}
	message := command()
	if calls != 1 {
		t.Fatalf("calls=%d message=%T", calls, message)
	}
	event, ok := message.(welcomeChangelogEvent)
	if !ok || len(event.bullets) != 1 || event.bullets[0] != "Added sessions" {
		t.Fatalf("message=%#v", message)
	}

	notReady := &model{ctx: context.Background(), runner: m.runner}
	if command = notReady.welcomeChangelogCmd(); command != nil {
		t.Fatal("nonblank-equivalent startup scheduled changelog fetch")
	}
	if calls != 1 {
		t.Fatalf("nonblank-equivalent startup fetched changelog calls=%d", calls)
	}
}
