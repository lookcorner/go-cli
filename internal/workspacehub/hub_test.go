package workspacehub

import (
	"context"
	"errors"
	"testing"
)

func TestCurrentFailClosed(t *testing.T) {
	t.Setenv(EnvUnlockRequest, "")
	t.Setenv(EnvHubURL, "")
	status := Current()
	if status.Enabled || status.Exposure || status.DialAllowed || status.ProductUnlocked {
		t.Fatalf("status=%#v", status)
	}
	if status.HubURL != DefaultHubURL {
		t.Fatalf("hub=%q", status.HubURL)
	}
	if len(status.BlockedReasons) == 0 || len(status.UnlockRequirements) == 0 {
		t.Fatalf("status=%#v", status)
	}
}

func TestDialAlwaysBlockedWithoutProductUnlock(t *testing.T) {
	t.Setenv(EnvUnlockRequest, "1")
	if err := Dial(context.Background(), DefaultHubURL); !errors.Is(err, ErrProductBlocked) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveHubURLEnv(t *testing.T) {
	t.Setenv(EnvHubURL, "wss://example.test/hub")
	if got := ResolveHubURL(); got != "wss://example.test/hub" {
		t.Fatalf("got=%q", got)
	}
}
