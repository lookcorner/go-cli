package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxRetainedRuns = 64

type RunSnapshot struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Objective string    `json:"objective"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	Revision  uint64    `json:"revision"`
	Phase     string    `json:"phase,omitempty"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type managedRun struct {
	snapshot RunSnapshot
	cancel   context.CancelFunc
}

type RunObserver interface {
	WorkflowRunUpdated(RunSnapshot)
}

// Manager owns session-scoped background workflow runs.
type Manager struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.RWMutex
	runs     map[string]*managedRun
	order    []string
	next     atomic.Uint64
	wg       sync.WaitGroup
	observer RunObserver
	execute  func(context.Context, Resolved, []byte, *Host) (string, error)
}

func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{ctx: ctx, cancel: cancel, runs: make(map[string]*managedRun), execute: func(ctx context.Context, resolved Resolved, args []byte, host *Host) (string, error) {
		return Execute(ctx, resolved, args, host)
	}}
}

func (m *Manager) Launch(resolved Resolved, args []byte, host *Host) RunSnapshot {
	args = append([]byte(nil), args...)
	now := time.Now().UTC()
	id := fmt.Sprintf("workflow-%d-%d", now.UnixMilli(), m.next.Add(1))
	ctx, cancel := context.WithCancel(m.ctx)
	run := &managedRun{snapshot: RunSnapshot{ID: id, Name: resolved.Name, Objective: runObjective(resolved.Name, args), Source: resolved.Source, Status: "running", Revision: 1, StartedAt: now, UpdatedAt: now}, cancel: cancel}
	if host == nil {
		host = &Host{}
	}
	previous := host.Callbacks
	host.Callbacks.OnPhase = func(title string, replayed bool) {
		m.update(id, func(snapshot *RunSnapshot) { snapshot.Phase = title })
		if previous.OnPhase != nil {
			previous.OnPhase(title, replayed)
		}
	}
	host.Callbacks.OnLog = previous.OnLog
	host.Callbacks.OnTelemetry = previous.OnTelemetry

	m.mu.Lock()
	m.runs[id] = run
	m.order = append([]string{id}, m.order...)
	m.trimLocked()
	started := run.snapshot
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		result, err := m.execute(ctx, resolved, args, host)
		m.update(id, func(snapshot *RunSnapshot) {
			snapshot.Status = "completed"
			snapshot.Result = result
			if err != nil {
				snapshot.Status = "failed"
				snapshot.Error = err.Error()
			}
			if ctx.Err() != nil {
				snapshot.Status = "cancelled"
				snapshot.Error = ""
			}
		})
	}()
	return started
}

func (m *Manager) SetObserver(observer RunObserver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.observer = observer
	m.mu.Unlock()
}

func (m *Manager) Snapshots() []RunSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]RunSnapshot, 0, len(m.order))
	for _, id := range m.order {
		if run := m.runs[id]; run != nil {
			items = append(items, run.snapshot)
		}
	}
	return items
}

func (m *Manager) Stop(id string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	run := m.runs[id]
	if run == nil || run.snapshot.Status != "running" {
		m.mu.RUnlock()
		return false
	}
	cancel := run.cancel
	m.mu.RUnlock()
	cancel()
	return true
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) update(id string, change func(*RunSnapshot)) {
	m.mu.Lock()
	var snapshot RunSnapshot
	if run := m.runs[id]; run != nil {
		change(&run.snapshot)
		run.snapshot.Revision++
		run.snapshot.UpdatedAt = time.Now().UTC()
		snapshot = run.snapshot
	}
	m.trimLocked()
	m.mu.Unlock()
	if snapshot.ID != "" {
		m.notify(snapshot)
	}
}

func runObjective(name string, args []byte) string {
	var values map[string]any
	if json.Unmarshal(args, &values) == nil {
		for _, key := range []string{"objective", "query"} {
			if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return name
}

func (m *Manager) notify(snapshot RunSnapshot) {
	m.mu.RLock()
	observer := m.observer
	m.mu.RUnlock()
	if observer != nil {
		observer.WorkflowRunUpdated(snapshot)
	}
}

func (m *Manager) trimLocked() {
	for len(m.order) > maxRetainedRuns {
		index := len(m.order) - 1
		id := m.order[index]
		if run := m.runs[id]; run != nil && run.snapshot.Status == "running" {
			return
		}
		delete(m.runs, id)
		m.order = m.order[:index]
	}
}
