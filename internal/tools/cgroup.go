package tools

import (
	"fmt"
	"sync/atomic"
)

// CgroupMemoryConfig configures Linux cgroup v2 memory limits for model-started
// shell children. Matches the Rust LocalTerminalBackend memory_high + headroom
// shape (cpu.max is not set).
type CgroupMemoryConfig struct {
	MemoryHighBytes uint64
	HeadroomBytes   uint64
}

// DefaultCgroupMemoryConfig returns 512 MiB soft / 256 MiB headroom hard ceiling.
func DefaultCgroupMemoryConfig() CgroupMemoryConfig {
	return CgroupMemoryConfig{
		MemoryHighBytes: 512 << 20,
		HeadroomBytes:   256 << 20,
	}
}

func (c CgroupMemoryConfig) memoryMax() uint64 {
	if c.MemoryHighBytes == 0 && c.HeadroomBytes == 0 {
		c = DefaultCgroupMemoryConfig()
	}
	return c.MemoryHighBytes + c.HeadroomBytes
}

func (c CgroupMemoryConfig) normalized() CgroupMemoryConfig {
	if c.MemoryHighBytes == 0 && c.HeadroomBytes == 0 {
		return DefaultCgroupMemoryConfig()
	}
	if c.MemoryHighBytes == 0 {
		c.MemoryHighBytes = DefaultCgroupMemoryConfig().MemoryHighBytes
	}
	if c.HeadroomBytes == 0 {
		c.HeadroomBytes = DefaultCgroupMemoryConfig().HeadroomBytes
	}
	return c
}

// shellCgroup confines spawned shell PIDs. Implementations are best-effort.
type shellCgroup interface {
	AddProcess(pid int) error
	Close() error
	Path() string
}

var cgroupSeq atomic.Uint64

func nextCgroupName() string {
	return fmt.Sprintf("gork-shell-%d", cgroupSeq.Add(1))
}
