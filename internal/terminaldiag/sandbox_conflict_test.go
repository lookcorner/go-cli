package terminaldiag

import (
	"strings"
	"testing"
)

func TestBuildSnapshotIncludesSandboxConflictFinding(t *testing.T) {
	probeSandboxConflict = func() string {
		return "Project and user sandbox settings define these profiles differently: 'dev'\n    note"
	}
	t.Cleanup(func() { probeSandboxConflict = func() string { return "" } })

	snapshot := BuildSnapshot(func(string) string { return "" }, func(string) (string, error) {
		return "", nil
	}, "linux")
	if snapshot.Counts.Issues < 1 {
		t.Fatalf("expected sandbox finding, got %#v", snapshot)
	}
	found := false
	for _, finding := range snapshot.Findings {
		if strings.Contains(finding, "sandbox settings define these profiles differently") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("findings=%v", snapshot.Findings)
	}
}
