package main

import (
	"context"
	"sync"

	"github.com/lookcorner/go-cli/internal/tools"
)

type scheduledWakeQueue struct {
	mu      sync.Mutex
	tasks   map[string]bool
	queued  map[string]bool
	pending []tools.ScheduledTaskFired
	active  string
	notify  chan struct{}
}

func newScheduledWakeQueue() *scheduledWakeQueue {
	return &scheduledWakeQueue{
		tasks: make(map[string]bool), queued: make(map[string]bool), notify: make(chan struct{}, 1),
	}
}

func (q *scheduledWakeQueue) ScheduledTaskCreated(event tools.ScheduledTaskCreated) {
	q.TrackWake(event.TaskID)
}

func (q *scheduledWakeQueue) TrackWake(id string) {
	q.mu.Lock()
	q.tasks[id] = true
	q.mu.Unlock()
	q.signal()
}

func (q *scheduledWakeQueue) ScheduledTaskFired(event tools.ScheduledTaskFired) {
	q.queueWake(event, false)
}

func (q *scheduledWakeQueue) QueueWake(id, prompt string) bool {
	return q.queueWake(tools.ScheduledTaskFired{TaskID: id, Prompt: prompt}, true)
}

func (q *scheduledWakeQueue) queueWake(event tools.ScheduledTaskFired, completed bool) bool {
	q.mu.Lock()
	if completed {
		delete(q.tasks, event.TaskID)
	}
	queued := q.active != event.TaskID && !q.queued[event.TaskID]
	if queued {
		q.queued[event.TaskID] = true
		q.pending = append(q.pending, event)
	}
	q.mu.Unlock()
	q.signal()
	return queued
}

func (q *scheduledWakeQueue) CancelWake(id string) {
	q.mu.Lock()
	delete(q.tasks, id)
	delete(q.queued, id)
	kept := q.pending[:0]
	for _, event := range q.pending {
		if event.TaskID != id {
			kept = append(kept, event)
		}
	}
	q.pending = kept
	q.mu.Unlock()
	q.signal()
}

func (q *scheduledWakeQueue) ScheduledTaskRemoved(taskID string) {
	q.mu.Lock()
	delete(q.tasks, taskID)
	q.mu.Unlock()
	q.signal()
}

func (q *scheduledWakeQueue) Take() (tools.ScheduledTaskFired, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return tools.ScheduledTaskFired{}, false
	}
	event := q.pending[0]
	q.pending = q.pending[1:]
	delete(q.queued, event.TaskID)
	q.active = event.TaskID
	return event, true
}

func (q *scheduledWakeQueue) Done(taskID string) {
	q.mu.Lock()
	if q.active == taskID {
		q.active = ""
	}
	q.mu.Unlock()
	q.signal()
}

func (q *scheduledWakeQueue) ShouldWait() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks) > 0 || len(q.pending) > 0 || q.active != ""
}

func (q *scheduledWakeQueue) Next(ctx context.Context) (tools.ScheduledTaskFired, bool, error) {
	for {
		if event, ok := q.Take(); ok {
			return event, true, nil
		}
		if !q.ShouldWait() {
			return tools.ScheduledTaskFired{}, false, nil
		}
		select {
		case <-q.notify:
		case <-ctx.Done():
			return tools.ScheduledTaskFired{}, false, ctx.Err()
		}
	}
}

func (q *scheduledWakeQueue) Notify() <-chan struct{} { return q.notify }

func (q *scheduledWakeQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
