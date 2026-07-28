//go:build !linux

package tools

func startMemoryHighMonitor(string, uint64) *memoryHighMonitor { return nil }

type memoryHighMonitor struct{}

func (m *memoryHighMonitor) TryRecv() *MemoryHighEvent { return nil }
func (m *memoryHighMonitor) Close()                    {}
