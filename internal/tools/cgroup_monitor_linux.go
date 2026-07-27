//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type memoryHighMonitor struct {
	mu        sync.Mutex
	pending   *MemoryHighEvent
	stop      chan struct{}
	done      chan struct{}
	threshold uint64
}

func startMemoryHighMonitor(cgroupPath string, memoryHighBytes uint64) *memoryHighMonitor {
	eventsPath := filepath.Join(cgroupPath, "memory.events")
	if _, err := os.Stat(eventsPath); err != nil {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	if err := watcher.Add(eventsPath); err != nil {
		_ = watcher.Close()
		return nil
	}
	mon := &memoryHighMonitor{
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		threshold: memoryHighBytes,
	}
	baseline, _ := readHighCounter(eventsPath)
	go mon.loop(watcher, cgroupPath, eventsPath, baseline)
	return mon
}

func (m *memoryHighMonitor) loop(watcher *fsnotify.Watcher, cgroupPath, eventsPath string, lastHigh uint64) {
	defer close(m.done)
	defer watcher.Close()
	currentPath := filepath.Join(cgroupPath, "memory.current")
	for {
		select {
		case <-m.stop:
			return
		case err, ok := <-watcher.Errors:
			if !ok || err != nil {
				return
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Chmod) == 0 {
				continue
			}
			high, ok := readHighCounter(eventsPath)
			if !ok || high <= lastHigh {
				continue
			}
			lastHigh = high
			current, ok := readMemoryCurrent(currentPath)
			if !ok {
				continue
			}
			buffer := memoryHighBuffer(m.threshold)
			if current < buffer {
				continue
			}
			ev := &MemoryHighEvent{MemoryCurrent: current, MemoryHighBytes: m.threshold}
			m.mu.Lock()
			m.pending = ev
			m.mu.Unlock()
		}
	}
}

func (m *memoryHighMonitor) TryRecv() *MemoryHighEvent {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.pending
	m.pending = nil
	return ev
}

func (m *memoryHighMonitor) Close() {
	if m == nil {
		return
	}
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
	}
}

func readHighCounter(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return parseMemoryEventsHigh(string(data))
}

func readMemoryCurrent(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
