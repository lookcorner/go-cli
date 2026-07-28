package workflow

import (
	"context"
	"testing"
	"time"
)

func TestManagerLaunchTracksBackgroundRun(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	release := make(chan struct{})
	manager.execute = func(ctx context.Context, _ Resolved, _ []byte, host *Host) (string, error) {
		host.Callbacks.OnPhase("Cross-check", false)
		select {
		case <-release:
			return "verified report", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	run := manager.Launch(Resolved{Name: "research", Source: "project"}, nil, &Host{})
	if run.ID == "" || run.Status != "running" {
		t.Fatalf("run=%+v", run)
	}
	wantRun(t, manager, run.ID, func(snapshot RunSnapshot) bool {
		return snapshot.Status == "running" && snapshot.Phase == "Cross-check"
	})
	close(release)
	wantRun(t, manager, run.ID, func(snapshot RunSnapshot) bool {
		return snapshot.Status == "completed" && snapshot.Result == "verified report"
	})
}

func TestManagerStopsRunningWorkflow(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	manager.execute = func(ctx context.Context, _ Resolved, _ []byte, _ *Host) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	run := manager.Launch(Resolved{Name: "long-run", Source: "builtin"}, nil, nil)
	if !manager.Stop(run.ID) || manager.Stop("missing") {
		t.Fatalf("stop run=%+v", run)
	}
	wantRun(t, manager, run.ID, func(snapshot RunSnapshot) bool {
		return snapshot.Status == "cancelled" && snapshot.Error == ""
	})
}

func wantRun(t *testing.T, manager *Manager, id string, ready func(RunSnapshot) bool) RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, snapshot := range manager.Snapshots() {
			if snapshot.ID == id && ready(snapshot) {
				return snapshot
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q not ready: %+v", id, manager.Snapshots())
	return RunSnapshot{}
}
