package leader

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKillTerminatesVerifiedProcessesAndCleansUnverifiedCandidates(t *testing.T) {
	root := t.TempDir()
	staleLock := filepath.Join(root, "leader.lock")
	staleSocket := filepath.Join(root, "leader.sock")
	if err := os.WriteFile(staleLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	livePID, stalePID, failedPID := uint32(10), uint32(20), uint32(30)
	var terminated []uint32
	result := kill([]Descriptor{
		{LiveInfo: &Info{PID: livePID}},
		{PIDFromLock: &stalePID, LockPath: staleLock, SocketPath: staleSocket},
		{LiveInfo: &Info{PID: failedPID}},
	}, func(pid uint32) bool {
		return pid != stalePID
	}, func(pid uint32) error {
		terminated = append(terminated, pid)
		if pid == failedPID {
			return errors.New("denied")
		}
		return nil
	}, os.Remove)
	if result.Killed != 1 || result.Cleaned != 1 || len(result.Outcomes) != 3 {
		t.Fatalf("result=%+v", result)
	}
	for index, action := range []KillAction{KillTerminated, KillCleaned, KillFailed} {
		if result.Outcomes[index].Action != action {
			t.Fatalf("outcome %d=%+v", index, result.Outcomes[index])
		}
	}
	if result.Outcomes[2].Error == nil {
		t.Fatal("failed termination did not retain its error")
	}
	if len(terminated) != 2 || terminated[0] != livePID || terminated[1] != failedPID {
		t.Fatalf("terminated=%v", terminated)
	}
	for _, path := range []string{staleLock, staleSocket} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was not removed: %v", path, err)
		}
	}
}

func TestKillSkipsDescriptorsWithoutPID(t *testing.T) {
	called := false
	result := kill([]Descriptor{{LockPath: "/tmp/leader.lock"}}, func(uint32) bool {
		called = true
		return true
	}, func(uint32) error {
		called = true
		return nil
	}, func(string) error {
		called = true
		return nil
	})
	if called || len(result.Outcomes) != 0 {
		t.Fatalf("called=%t result=%+v", called, result)
	}
}

func TestIsGorkExecutableChecksOnlyTheExecutableName(t *testing.T) {
	for _, path := range []string{"/usr/local/bin/gork", "/opt/tools/go-cli.exe"} {
		if !isGorkExecutable(path) {
			t.Fatalf("%q was not recognized", path)
		}
	}
	for _, path := range []string{"/tmp/gork-workspace/unrelated", "/usr/bin/worker"} {
		if isGorkExecutable(path) {
			t.Fatalf("%q must not identify an unrelated executable", path)
		}
	}
}
