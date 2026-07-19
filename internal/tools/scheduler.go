package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
)

const (
	minimumScheduleInterval = time.Minute
	maximumScheduledTasks   = 50
	scheduleLifetime        = 7 * 24 * time.Hour
)

type ScheduledTask struct {
	ID           string     `json:"id"`
	IntervalSecs uint64     `json:"intervalSecs"`
	Prompt       string     `json:"prompt"`
	Recurring    bool       `json:"recurring"`
	Durable      bool       `json:"durable"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastFiredAt  *time.Time `json:"lastFiredAt,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

func (t ScheduledTask) NextFireAt() time.Time {
	anchor := t.CreatedAt
	if t.LastFiredAt != nil {
		anchor = *t.LastFiredAt
	}
	return anchor.Add(time.Duration(t.IntervalSecs) * time.Second)
}

type ScheduledTaskCreated struct {
	TaskID        string  `json:"task_id"`
	Prompt        string  `json:"prompt"`
	HumanSchedule string  `json:"human_schedule"`
	NextFireAt    *string `json:"next_fire_at"`
}

type ScheduledTaskFired = ScheduledTaskCreated

type SchedulerObserver interface {
	ScheduledTaskCreated(ScheduledTaskCreated)
	ScheduledTaskFired(ScheduledTaskFired)
	ScheduledTaskRemoved(string)
}

type schedulerEvent struct {
	kind    string
	created ScheduledTaskCreated
	taskID  string
}

type Scheduler struct {
	mu       sync.Mutex
	tasks    []ScheduledTask
	state    string
	observer SchedulerObserver
	pending  []schedulerEvent
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	started  bool
	closed   bool
}

func NewScheduler() *Scheduler {
	return &Scheduler{wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *Scheduler) Configure(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("scheduler state path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read scheduler state: %w", err)
	}
	var tasks []ScheduledTask
	if len(data) > 0 {
		if err := json.Unmarshal(data, &tasks); err != nil {
			return fmt.Errorf("decode scheduler state: %w", err)
		}
	}
	if len(tasks) > maximumScheduledTasks {
		return fmt.Errorf("scheduler state exceeds maximum of %d tasks", maximumScheduledTasks)
	}
	for _, task := range tasks {
		if task.ID == "" || task.IntervalSecs == 0 || !task.Durable {
			return errors.New("scheduler state contains an invalid task")
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("scheduler is closed")
	}
	s.startLocked()
	s.state, s.tasks = path, tasks
	now := time.Now().UTC()
	kept := s.tasks[:0]
	for _, task := range s.tasks {
		if !task.Recurring && task.LastFiredAt == nil && task.NextFireAt().Before(now) {
			payload := scheduledPayload(task)
			payload.NextFireAt = nil
			s.pending = append(s.pending,
				schedulerEvent{kind: "fired", created: payload},
				schedulerEvent{kind: "removed", taskID: task.ID},
			)
			continue
		}
		kept = append(kept, task)
	}
	s.tasks = kept
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("save restored scheduler state: %w", err)
	}
	s.mu.Unlock()
	s.signal()
	return nil
}

func (s *Scheduler) SetObserver(observer SchedulerObserver) {
	if observer == nil {
		s.mu.Lock()
		s.observer = nil
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.observer = observer
	pending := append([]schedulerEvent(nil), s.pending...)
	s.pending = nil
	announced := make(map[string]bool)
	for _, event := range pending {
		if event.kind == "created" {
			announced[event.created.TaskID] = true
		}
	}
	for _, task := range s.tasks {
		if !announced[task.ID] {
			pending = append(pending, schedulerEvent{kind: "created", created: scheduledPayload(task)})
		}
	}
	s.mu.Unlock()
	for _, event := range pending {
		s.deliver(observer, event)
	}
}

func (s *Scheduler) Create(interval time.Duration, prompt string, recurring, durable, fireImmediately bool) (ScheduledTask, error) {
	now := time.Now().UTC()
	created := now
	if fireImmediately {
		created = now.Add(-interval)
	}
	task := ScheduledTask{
		ID: schedulerID(), IntervalSecs: uint64(interval / time.Second), Prompt: prompt,
		Recurring: recurring, Durable: durable, CreatedAt: created,
	}
	if recurring {
		expires := now.Add(scheduleLifetime)
		task.ExpiresAt = &expires
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ScheduledTask{}, errors.New("scheduler is closed")
	}
	s.startLocked()
	if len(s.tasks) >= maximumScheduledTasks {
		s.mu.Unlock()
		return ScheduledTask{}, fmt.Errorf("maximum of %d scheduled tasks reached", maximumScheduledTasks)
	}
	s.tasks = append(s.tasks, task)
	err := s.saveLocked()
	if err != nil {
		s.tasks = s.tasks[:len(s.tasks)-1]
	}
	s.mu.Unlock()
	if err != nil {
		return ScheduledTask{}, err
	}
	s.emit(schedulerEvent{kind: "created", created: scheduledPayload(task)})
	s.signal()
	return task, nil
}

func (s *Scheduler) Delete(id string) (bool, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, errors.New("scheduler is closed")
	}
	index := -1
	for i := range s.tasks {
		if s.tasks[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return false, nil
	}
	removedTask := s.tasks[index]
	s.tasks = append(s.tasks[:index], s.tasks[index+1:]...)
	err := s.saveLocked()
	if err != nil {
		s.tasks = append(s.tasks, ScheduledTask{})
		copy(s.tasks[index+1:], s.tasks[index:])
		s.tasks[index] = removedTask
	}
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	s.emit(schedulerEvent{kind: "removed", taskID: id})
	s.signal()
	return true, nil
}

func (s *Scheduler) List() []ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ScheduledTask(nil), s.tasks...)
}

func (s *Scheduler) run() {
	defer close(s.done)
	for {
		delay := s.nextDelay()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			s.fireDue()
		case <-s.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-s.stop:
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func (s *Scheduler) startLocked() {
	if !s.started {
		s.started = true
		go s.run()
	}
}

func (s *Scheduler) nextDelay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return 24 * time.Hour
	}
	next := s.tasks[0].NextFireAt()
	for _, task := range s.tasks[1:] {
		if candidate := task.NextFireAt(); candidate.Before(next) {
			next = candidate
		}
	}
	return max(time.Until(next), 0)
}

func (s *Scheduler) fireDue() {
	now := time.Now().UTC()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	index := -1
	for i := range s.tasks {
		if !s.tasks[i].NextFireAt().After(now) {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return
	}
	task := s.tasks[index]
	task.LastFiredAt = &now
	remove := !task.Recurring || task.ExpiresAt != nil && !now.Before(*task.ExpiresAt)
	if remove {
		s.tasks = append(s.tasks[:index], s.tasks[index+1:]...)
	} else {
		s.tasks[index] = task
	}
	_ = s.saveLocked()
	s.mu.Unlock()
	payload := scheduledPayload(task)
	s.emit(schedulerEvent{kind: "fired", created: payload})
	if remove {
		s.emit(schedulerEvent{kind: "removed", taskID: task.ID})
	}
}

func (s *Scheduler) saveLocked() error {
	if s.state == "" {
		return nil
	}
	durable := make([]ScheduledTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		if task.Durable {
			durable = append(durable, task)
		}
	}
	data, err := json.Marshal(durable)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.state), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.state), ".scheduler-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceSchedulerState(name, s.state)
}

func (s *Scheduler) emit(event schedulerEvent) {
	s.mu.Lock()
	observer := s.observer
	if observer == nil {
		s.pending = append(s.pending, event)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.deliver(observer, event)
}

func (s *Scheduler) deliver(observer SchedulerObserver, event schedulerEvent) {
	if observer == nil {
		return
	}
	switch event.kind {
	case "created":
		observer.ScheduledTaskCreated(event.created)
	case "fired":
		observer.ScheduledTaskFired(event.created)
	case "removed":
		observer.ScheduledTaskRemoved(event.taskID)
	}
}

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	s.mu.Unlock()
	if started {
		close(s.stop)
		<-s.done
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.tasks))
	for _, task := range s.tasks {
		ids = append(ids, task.ID)
	}
	err := s.saveLocked()
	s.mu.Unlock()
	for _, id := range ids {
		s.emit(schedulerEvent{kind: "removed", taskID: id})
	}
	return err
}

func scheduledPayload(task ScheduledTask) ScheduledTaskCreated {
	next := task.NextFireAt().Format(time.RFC3339Nano)
	return ScheduledTaskCreated{
		TaskID: task.ID, Prompt: task.Prompt, HumanSchedule: intervalToHuman(time.Duration(task.IntervalSecs) * time.Second), NextFireAt: &next,
	}
}

func schedulerID() string {
	data := make([]byte, 6)
	if _, err := rand.Read(data); err == nil {
		return hex.EncodeToString(data)
	}
	return fmt.Sprintf("%012x", time.Now().UnixNano())[:12]
}

func parseScheduleInterval(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return 0, errors.New("invalid interval format (expected e.g. 5m, 2h, 1d)")
	}
	number, err := strconv.ParseUint(value[:len(value)-1], 10, 64)
	if err != nil || number == 0 {
		return 0, fmt.Errorf("invalid interval format: %q (expected e.g. 5m, 2h, 1d)", value)
	}
	unit := map[byte]uint64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[value[len(value)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("invalid interval suffix: %q (expected s, m, h, or d)", value[len(value)-1:])
	}
	if number > uint64((time.Duration(1<<63-1)/time.Second))/unit {
		return 0, fmt.Errorf("interval too large: %q", value)
	}
	return max(time.Duration(number*unit)*time.Second, minimumScheduleInterval), nil
}

func intervalToHuman(interval time.Duration) string {
	seconds := uint64(interval / time.Second)
	for _, unit := range []struct {
		seconds uint64
		name    string
	}{{86400, "day"}, {3600, "hour"}, {60, "minute"}} {
		if seconds%unit.seconds == 0 {
			count := seconds / unit.seconds
			suffix := unit.name
			if count != 1 {
				suffix += "s"
			}
			return fmt.Sprintf("every %d %s", count, suffix)
		}
	}
	suffix := "seconds"
	if seconds == 1 {
		suffix = "second"
	}
	return fmt.Sprintf("every %d %s", seconds, suffix)
}

type schedulerCreateTool struct{ scheduler *Scheduler }

func (t *schedulerCreateTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "scheduler_create",
		Description: "Create a scheduled prompt. Intervals use <number><s|m|h|d>, with a 60-second minimum, a 50-task limit, and 7-day expiry for recurring tasks.",
		Parameters: objectSchema(map[string]any{
			"interval":         map[string]any{"type": "string"},
			"prompt":           map[string]any{"type": "string"},
			"recurring":        map[string]any{"type": "boolean", "default": true},
			"durable":          map[string]any{"type": "boolean", "default": false},
			"fire_immediately": map[string]any{"type": "boolean", "default": false},
		}, "interval", "prompt"),
	}
}

func (t *schedulerCreateTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Interval        string `json:"interval"`
		Prompt          string `json:"prompt"`
		Recurring       *bool  `json:"recurring"`
		Durable         bool   `json:"durable"`
		FireImmediately bool   `json:"fire_immediately"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode scheduler_create arguments: %w", err)
	}
	interval, err := parseScheduleInterval(args.Interval)
	if err != nil {
		return "", err
	}
	recurring := args.Recurring == nil || *args.Recurring
	task, err := t.scheduler.Create(interval, args.Prompt, recurring, args.Durable, args.FireImmediately)
	if err != nil {
		return "", err
	}
	return encodeSchedulerOutput(map[string]any{"id": task.ID, "humanSchedule": intervalToHuman(interval), "recurring": recurring})
}

type schedulerListTool struct{ scheduler *Scheduler }

func (t *schedulerListTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: "scheduler_list", Description: "List active scheduled tasks.", Parameters: objectSchema(nil)}
}

func (t *schedulerListTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	tasks := t.scheduler.List()
	summaries := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		prompt := task.Prompt
		if len(prompt) > 80 {
			end := 80
			for end > 0 && !utf8.RuneStart(prompt[end]) {
				end--
			}
			prompt = prompt[:end] + "..."
		}
		summaries = append(summaries, map[string]any{
			"id": task.ID, "prompt": prompt, "intervalHuman": intervalToHuman(time.Duration(task.IntervalSecs) * time.Second),
			"nextFireAt": task.NextFireAt().Format(time.RFC3339Nano), "createdAt": task.CreatedAt.Format(time.RFC3339Nano), "recurring": task.Recurring,
		})
	}
	return encodeSchedulerOutput(map[string]any{"tasks": summaries})
}

type schedulerDeleteTool struct{ scheduler *Scheduler }

func (t *schedulerDeleteTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "scheduler_delete", Description: "Cancel a scheduled task by ID.",
		Parameters: objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, "id"),
	}
}

func (t *schedulerDeleteTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode scheduler_delete arguments: %w", err)
	}
	removed, err := t.scheduler.Delete(args.ID)
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("Scheduled task %s cancelled.", args.ID)
	if !removed {
		message = fmt.Sprintf("No scheduled task with ID %s found. Use scheduler_list to see active tasks.", args.ID)
	}
	return encodeSchedulerOutput(map[string]any{"success": removed, "message": message})
}

func encodeSchedulerOutput(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

func ExpandLoopCommand(prompt string) (string, bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed != "/loop" && !strings.HasPrefix(trimmed, "/loop ") && !strings.HasPrefix(trimmed, "/loop\t") {
		return prompt, false
	}
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, "/loop"))
	if args == "" {
		return "Usage: /loop [interval] <prompt>\nExample: /loop 30m check deploy status\nExample: /loop check deploy status every hour\n\nTell me how often it should run (e.g. 30m, 1 hour, every 2 days).", true
	}
	return "# /loop -- schedule a recurring prompt\n\n" +
		"Parse the input below into an interval and a prompt, then schedule it with scheduler_create.\n\n" +
		"## Deriving the interval\n" +
		"Read how often to run from the user's request -- however they phrase it -- and convert it to a compact `<number><unit>` string, where unit is one of `s` (seconds), `m` (minutes), `h` (hours), or `d` (days). The interval may appear at the start or end of the request; extract it and use the remaining text as the prompt.\n\n" +
		"The minimum interval is 60 seconds; shorter values are raised to 60s, so tell the user if that applies.\n\n" +
		"If the request contains no interval at all, ask the user how often it should run before scheduling. Do NOT invent or assume a default interval.\n\n" +
		"## Action\n" +
		"1. Call scheduler_create with: interval (the compact string you derived), prompt, recurring: true, fire_immediately: true. If the interval is unparseable, fix the interval string rather than guessing.\n" +
		"2. Confirm what is scheduled, the cadence, that it auto-expires after 7 days, and that it can be cancelled with scheduler_delete (include the job ID).\n" +
		"3. Do NOT execute the prompt inline. The scheduler will fire it immediately.\n\n" +
		"## Input\n" + args, true
}
