//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateShellCgroupWritesMemoryLimits(t *testing.T) {
	root := t.TempDir()
	sysfs := filepath.Join(root, "sys", "fs", "cgroup")
	selfDir := filepath.Join(sysfs, "user.slice")
	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cgroup.controllers", "cgroup.subtree_control"} {
		if err := os.WriteFile(filepath.Join(sysfs, name), []byte("memory cpu"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(selfDir, name), []byte("memory cpu"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(selfDir, "cgroup.procs"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc", "self", "cgroup")
	if err := os.MkdirAll(filepath.Dir(proc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proc, []byte("0::/user.slice\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := CgroupMemoryConfig{MemoryHighBytes: 1000, HeadroomBytes: 200}
	guard, err := createShellCgroup(sysfs, proc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if !strings.Contains(guard.Path(), filepath.Join(sysfs, "user.slice", "gork-shell-")) {
		t.Fatalf("path=%q", guard.Path())
	}
	high, err := os.ReadFile(filepath.Join(guard.Path(), "memory.high"))
	if err != nil || string(high) != "1000" {
		t.Fatalf("memory.high=%q err=%v", high, err)
	}
	max, err := os.ReadFile(filepath.Join(guard.Path(), "memory.max"))
	if err != nil || string(max) != "1200" {
		t.Fatalf("memory.max=%q err=%v", max, err)
	}
	if err := guard.AddProcess(42); err != nil {
		t.Fatal(err)
	}
	procs, err := os.ReadFile(filepath.Join(guard.Path(), "cgroup.procs"))
	if err != nil || string(procs) != "42" {
		t.Fatalf("cgroup.procs=%q err=%v", procs, err)
	}
	parentCtl, _ := os.ReadFile(filepath.Join(selfDir, "cgroup.subtree_control"))
	if !strings.Contains(string(parentCtl), "memory") {
		t.Fatalf("subtree_control=%q", parentCtl)
	}
}

func TestCreateShellCgroupSkipsWithoutV2(t *testing.T) {
	root := t.TempDir()
	sysfs := filepath.Join(root, "sys", "fs", "cgroup")
	if err := os.MkdirAll(sysfs, 0o755); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc", "self", "cgroup")
	if err := os.MkdirAll(filepath.Dir(proc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proc, []byte("1:name=systemd:/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := createShellCgroup(sysfs, proc, DefaultCgroupMemoryConfig()); err == nil {
		t.Fatal("expected failure without cgroup v2")
	}
}

func TestTryShellCgroupNoopWhenUnavailable(t *testing.T) {
	oldSysfs, oldProc := cgroupSysfsRoot, cgroupSelfProc
	t.Cleanup(func() { cgroupSysfsRoot, cgroupSelfProc = oldSysfs, oldProc })
	root := t.TempDir()
	cgroupSysfsRoot = filepath.Join(root, "missing")
	cgroupSelfProc = filepath.Join(root, "missing-proc")
	if guard := tryShellCgroup(DefaultCgroupMemoryConfig()); guard != nil {
		t.Fatalf("expected nil guard, got %#v", guard)
	}
}
