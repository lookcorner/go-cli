package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingSchedulerObserver struct {
	mu      sync.Mutex
	created []ScheduledTaskCreated
	fired   chan ScheduledTaskFired
	removed chan string
}

func (o *recordingSchedulerObserver) ScheduledTaskCreated(event ScheduledTaskCreated) {
	o.mu.Lock()
	o.created = append(o.created, event)
	o.mu.Unlock()
}

func (o *recordingSchedulerObserver) ScheduledTaskFired(event ScheduledTaskFired) {
	o.fired <- event
}

func (o *recordingSchedulerObserver) ScheduledTaskRemoved(taskID string) {
	o.removed <- taskID
}

func TestScheduleIntervalContract(t *testing.T) {
	tests := map[string]time.Duration{
		"30s": time.Minute, "5m": 5 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour,
	}
	for input, want := range tests {
		got, err := parseScheduleInterval(input)
		if err != nil || got != want {
			t.Fatalf("parseScheduleInterval(%q)=%s,%v want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0m", "5x", "abc", "1000000000000000000d"} {
		if _, err := parseScheduleInterval(input); err == nil {
			t.Fatalf("invalid interval %q was accepted", input)
		}
	}
	if intervalToHuman(2*time.Hour) != "every 2 hours" || intervalToHuman(time.Minute) != "every 1 minute" {
		t.Fatal("human interval formatting changed")
	}
}

func TestExpandLoopCommandUsesCanonicalSchedulingInstruction(t *testing.T) {
	usage, ok := ExpandLoopCommand("/loop")
	if !ok || !strings.Contains(usage, "Usage: /loop") || strings.Contains(usage, "10m") {
		t.Fatalf("usage=%q ok=%v", usage, ok)
	}
	expanded, ok := ExpandLoopCommand("/loop every 30 minutes check deploy")
	if !ok || !strings.Contains(expanded, "scheduler_create") || !strings.Contains(expanded, "<number><unit>") || !strings.Contains(expanded, "every 30 minutes check deploy") || !strings.Contains(expanded, "Do NOT execute the prompt inline") {
		t.Fatalf("expanded=%q ok=%v", expanded, ok)
	}
	if unchanged, ok := ExpandLoopCommand("/loopy test"); ok || unchanged != "/loopy test" {
		t.Fatalf("non-command=%q ok=%v", unchanged, ok)
	}
}

func TestSchedulerToolsFireAndRemoveOneShot(t *testing.T) {
	scheduler := NewScheduler()
	defer scheduler.Close()
	observer := &recordingSchedulerObserver{fired: make(chan ScheduledTaskFired, 1), removed: make(chan string, 1)}
	scheduler.SetObserver(observer)
	output, err := (&schedulerCreateTool{scheduler: scheduler}).Execute(context.Background(), json.RawMessage(
		`{"interval":"1m","prompt":"check deployment","recurring":false,"fire_immediately":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if json.Unmarshal([]byte(output), &created) != nil || len(created["id"].(string)) != 12 || created["humanSchedule"] != "every 1 minute" || created["recurring"] != false {
		t.Fatalf("create output=%s", output)
	}
	observer.mu.Lock()
	if len(observer.created) != 1 || observer.created[0].Prompt != "check deployment" {
		observer.mu.Unlock()
		t.Fatalf("created events=%#v", observer.created)
	}
	taskID := observer.created[0].TaskID
	observer.mu.Unlock()
	select {
	case event := <-observer.fired:
		if event.TaskID != taskID || event.Prompt != "check deployment" {
			t.Fatalf("fired=%#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("fire_immediately task did not fire")
	}
	select {
	case removed := <-observer.removed:
		if removed != taskID {
			t.Fatalf("removed=%q", removed)
		}
	case <-time.After(time.Second):
		t.Fatal("one-shot task was not removed")
	}
	listed, err := (&schedulerListTool{scheduler: scheduler}).Execute(context.Background(), nil)
	if err != nil || listed != `{"tasks":[]}` {
		t.Fatalf("list=%s err=%v", listed, err)
	}
}

func TestSchedulerPersistsOnlyDurableTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.json")
	first := NewScheduler()
	if err := first.Configure(path); err != nil {
		t.Fatal(err)
	}
	durable, err := first.Create(time.Hour, strings.Repeat("世", 90), true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Create(time.Hour, "ephemeral", true, false, false); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := NewScheduler()
	defer second.Close()
	if err := second.Configure(path); err != nil {
		t.Fatal(err)
	}
	tasks := second.List()
	if len(tasks) != 1 || tasks[0].ID != durable.ID || !tasks[0].Durable {
		t.Fatalf("restored tasks=%#v", tasks)
	}
	listed, err := (&schedulerListTool{scheduler: second}).Execute(context.Background(), nil)
	if err != nil || !strings.Contains(listed, "...") || strings.Contains(listed, "ephemeral") {
		t.Fatalf("list=%s err=%v", listed, err)
	}
	deleted, err := second.Delete(durable.ID)
	if err != nil || !deleted {
		t.Fatalf("delete=%v err=%v", deleted, err)
	}
	if deleted, err := second.Delete(durable.ID); err != nil || deleted {
		t.Fatalf("second delete=%v err=%v", deleted, err)
	}
}

func TestSchedulerEnforcesTaskLimit(t *testing.T) {
	scheduler := NewScheduler()
	defer scheduler.Close()
	seen := make(map[string]bool)
	for index := 0; index < maximumScheduledTasks; index++ {
		task, err := scheduler.Create(time.Hour, "task", true, false, false)
		if err != nil || len(task.ID) != 12 || seen[task.ID] {
			t.Fatalf("create %d task=%#v err=%v", index, task, err)
		}
		seen[task.ID] = true
	}
	if _, err := scheduler.Create(time.Hour, "overflow", true, false, false); err == nil || !strings.Contains(err.Error(), "maximum of 50") {
		t.Fatalf("limit error=%v", err)
	}
}

func TestRegistryScheduledTasksReturnsDisplaySnapshot(t *testing.T) {
	scheduler := NewScheduler()
	defer scheduler.Close()
	task, err := scheduler.Create(5*time.Minute, "check deployment", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{scheduler: scheduler}
	listed := registry.ScheduledTasks()
	if len(listed) != 1 || listed[0].TaskID != task.ID || listed[0].Prompt != "check deployment" || listed[0].HumanSchedule != "every 5 minutes" || listed[0].NextFireAt == nil {
		t.Fatalf("tasks=%#v", listed)
	}
	if got := (*Registry)(nil).ScheduledTasks(); got != nil {
		t.Fatalf("nil registry tasks=%#v", got)
	}
}

func TestSchedulerRestoresMissedOneShotWithoutGhostCreatedEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.json")
	created := time.Now().UTC().Add(-2 * time.Minute)
	task := ScheduledTask{ID: "missed-task1", IntervalSecs: 60, Prompt: "missed", Durable: true, CreatedAt: created}
	data, err := json.Marshal([]ScheduledTask{task})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler()
	defer scheduler.Close()
	if err := scheduler.Configure(path); err != nil {
		t.Fatal(err)
	}
	observer := &recordingSchedulerObserver{fired: make(chan ScheduledTaskFired, 1), removed: make(chan string, 1)}
	scheduler.SetObserver(observer)
	observer.mu.Lock()
	createdCount := len(observer.created)
	observer.mu.Unlock()
	if createdCount != 0 {
		t.Fatalf("missed one-shot was announced as active: %#v", observer.created)
	}
	if fired := <-observer.fired; fired.TaskID != task.ID || fired.NextFireAt != nil {
		t.Fatalf("fired=%#v", fired)
	}
	if removed := <-observer.removed; removed != task.ID || len(scheduler.List()) != 0 {
		t.Fatalf("removed=%q tasks=%#v", removed, scheduler.List())
	}
}
