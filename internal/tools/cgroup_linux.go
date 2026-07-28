//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultCgroupSysfs = "/sys/fs/cgroup"
	defaultSelfCgroup  = "/proc/self/cgroup"
)

// test hooks (overridden in unit tests).
var (
	cgroupSysfsRoot = defaultCgroupSysfs
	cgroupSelfProc  = defaultSelfCgroup
)

type linuxShellCgroup struct {
	path string
	mon  *memoryHighMonitor
}

func tryShellCgroup(cfg CgroupMemoryConfig) shellCgroup {
	guard, err := createShellCgroup(cgroupSysfsRoot, cgroupSelfProc, cfg.normalized())
	if err != nil {
		return nil
	}
	return guard
}

func createShellCgroup(sysfsRoot, selfProc string, cfg CgroupMemoryConfig) (*linuxShellCgroup, error) {
	selfPath, err := readSelfCgroupV2(selfProc)
	if err != nil {
		return nil, err
	}
	if !cgroupV2Available(sysfsRoot, selfPath) {
		return nil, fmt.Errorf("cgroup v2 unavailable under %s", sysfsRoot)
	}
	name := nextCgroupName()
	path := filepath.Join(sysfsRoot, strings.TrimPrefix(selfPath, "/"), name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	_ = os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+memory +cpu"), 0o644)
	if err := os.WriteFile(filepath.Join(path, "memory.high"), []byte(fmt.Sprintf("%d", cfg.MemoryHighBytes)), 0o644); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write memory.high: %w", err)
	}
	if err := os.WriteFile(filepath.Join(path, "memory.max"), []byte(fmt.Sprintf("%d", cfg.memoryMax())), 0o644); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write memory.max: %w", err)
	}
	guard := &linuxShellCgroup{path: path}
	guard.mon = startMemoryHighMonitor(path, cfg.MemoryHighBytes)
	return guard, nil
}

func (g *linuxShellCgroup) AddProcess(pid int) error {
	if g == nil || pid <= 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(g.path, "cgroup.procs"), []byte(fmt.Sprintf("%d", pid)), 0o644)
}

func (g *linuxShellCgroup) Close() error {
	if g == nil || g.path == "" {
		return nil
	}
	if g.mon != nil {
		g.mon.Close()
		g.mon = nil
	}
	_ = os.WriteFile(filepath.Join(g.path, "cgroup.kill"), []byte("1"), 0o644)
	err := os.Remove(g.path)
	g.path = ""
	return err
}

func (g *linuxShellCgroup) Path() string {
	if g == nil {
		return ""
	}
	return g.path
}

func (g *linuxShellCgroup) TryRecvMemoryHigh() *MemoryHighEvent {
	if g == nil || g.mon == nil {
		return nil
	}
	return g.mon.TryRecv()
}

func readSelfCgroupV2(procFile string) (string, error) {
	data, err := os.ReadFile(procFile)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry in %s", procFile)
}

func cgroupV2Available(sysfsRoot, selfPath string) bool {
	controllers := filepath.Join(sysfsRoot, "cgroup.controllers")
	if _, err := os.Stat(controllers); err != nil {
		return false
	}
	parent := filepath.Join(sysfsRoot, strings.TrimPrefix(selfPath, "/"))
	if selfPath == "/" || selfPath == "" {
		parent = sysfsRoot
	}
	if _, err := os.Stat(filepath.Join(parent, "cgroup.subtree_control")); err != nil {
		return false
	}
	return true
}
