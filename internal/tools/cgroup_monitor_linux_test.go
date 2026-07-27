//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryHighMonitorFiresOnHighIncrement(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "memory.events")
	current := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(events, []byte("high 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte("950\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mon := startMemoryHighMonitor(dir, 1000)
	if mon == nil {
		t.Fatal("expected monitor")
	}
	defer mon.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := os.WriteFile(current, []byte("950\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(events, []byte("high 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ev := mon.TryRecv(); ev != nil {
			if ev.MemoryCurrent != 950 || ev.MemoryHighBytes != 1000 {
				t.Fatalf("event=%#v", ev)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("monitor did not fire")
}
