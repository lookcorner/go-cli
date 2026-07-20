package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

const memoryFlushPrompt = `You are a memory assistant. Extract useful information from this conversation that would help in future sessions with this user. Write concise markdown with # or ## headers covering substantive decisions and rationale, technical context, debugging techniques, and problems with their solutions. Omit empty sections, user environment preferences, and ephemeral progress. Respond with NO_REPLY when nothing genuinely reusable was learned.`

type MemoryFlushResult struct {
	Outcome string
	Path    string
}

func (r *Runner) injectMemoryContext(content any, previousResponseID string) any {
	r.memoryMu.Lock()
	if r.memoryInjected {
		r.memoryMu.Unlock()
		return content
	}
	r.memoryInjected = true
	enabled := previousResponseID == "" && r.Memory != nil && r.MemoryConfig.Enabled && r.MemoryConfig.InitialInjection
	r.memoryMu.Unlock()
	if !enabled {
		return content
	}
	value, err := r.Memory.Context()
	if err != nil || value == "" {
		if err != nil {
			r.log("memory_context_error", map[string]any{"error": err.Error()})
		}
		return content
	}
	r.log("memory_context_injected", map[string]any{"characters": len([]rune(value))})
	switch current := content.(type) {
	case string:
		return value + "\n\n" + current
	case []api.ContentPart:
		return append([]api.ContentPart{{Type: "input_text", Text: value}}, current...)
	default:
		return content
	}
}

func (r *Runner) maybeStartMemoryFlush(ctx context.Context, previousResponseID string) {
	if !r.shouldFlushMemory(previousResponseID) {
		return
	}
	r.memoryMu.Lock()
	if r.memoryFlushArmed || r.memoryFlushRunning {
		r.memoryMu.Unlock()
		return
	}
	r.memoryFlushArmed, r.memoryFlushRunning = true, true
	r.memoryFlushDone = make(chan struct{})
	previous := r.memoryLastFlush
	count := r.memoryFlushCount
	r.memoryMu.Unlock()
	streamer := r.compactionStreamer(true)
	go func() {
		_, _ = r.runMemoryFlush(ctx, streamer, previousResponseID, previous, count, "pre_compaction")
	}()
}

func (r *Runner) shouldFlushMemory(previousResponseID string) bool {
	config := r.MemoryConfig.Flush
	if r.Memory == nil || !r.MemoryConfig.Enabled || !config.Enabled || previousResponseID == "" || r.ContextWindow <= 0 || r.lastInputTokens <= 0 {
		return false
	}
	compactAt := int64(r.ContextWindow) * int64(r.CompactThresholdPercent)
	flushAt := compactAt - int64(config.SoftThresholdTokens)*100
	return int64(r.lastInputTokens)*100 >= max(int64(0), flushAt)
}

func (r *Runner) FlushMemory(ctx context.Context, previousResponseID string) (MemoryFlushResult, error) {
	r.cancelMemoryIdleFlush()
	return r.flushMemory(ctx, previousResponseID, "user_requested")
}

func (r *Runner) flushMemory(ctx context.Context, previousResponseID, trigger string) (MemoryFlushResult, error) {
	if r.Client == nil {
		return MemoryFlushResult{}, errors.New("model client is unavailable")
	}
	if r.Memory == nil || !r.MemoryConfig.Enabled || !r.MemoryConfig.Flush.Enabled {
		return MemoryFlushResult{}, errors.New("memory is not enabled for this session")
	}
	if previousResponseID == "" {
		return MemoryFlushResult{}, errors.New("no completed response is available to flush")
	}
	r.memoryMu.Lock()
	if r.memoryFlushRunning {
		r.memoryMu.Unlock()
		return MemoryFlushResult{}, errors.New("another memory flush is already in progress")
	}
	r.memoryFlushArmed, r.memoryFlushRunning = true, true
	r.memoryFlushDone = make(chan struct{})
	previous, count := r.memoryLastFlush, r.memoryFlushCount
	r.memoryMu.Unlock()
	return r.runMemoryFlush(ctx, r.compactionStreamer(true), previousResponseID, previous, count, trigger)
}

func (r *Runner) cancelMemoryIdleFlush() {
	r.memoryMu.Lock()
	cancel := r.memoryIdleCancel
	r.memoryIdleCancel = nil
	r.memoryMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) scheduleMemoryIdleFlush(ctx context.Context, previousResponseID string) {
	seconds := r.MemoryConfig.Flush.IdleTimeoutSeconds
	if seconds == nil || r.Memory == nil || !r.MemoryConfig.Enabled || !r.MemoryConfig.Flush.Enabled || previousResponseID == "" || *seconds > uint64((1<<63-1)/int64(time.Second)) {
		return
	}
	timerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.memoryMu.Lock()
	previousCancel := r.memoryIdleCancel
	r.memoryIdleCancel, r.memoryIdleDone = cancel, done
	r.memoryMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	go func() {
		defer func() {
			r.memoryMu.Lock()
			if r.memoryIdleDone == done {
				r.memoryIdleCancel, r.memoryIdleDone = nil, nil
			}
			close(done)
			r.memoryMu.Unlock()
		}()
		timer := time.NewTimer(time.Duration(*seconds) * time.Second)
		defer timer.Stop()
		select {
		case <-timerCtx.Done():
			return
		case <-timer.C:
		}
		r.memoryMu.Lock()
		current := r.memoryIdleDone == done
		if current {
			r.memoryIdleCancel = nil
		}
		r.memoryMu.Unlock()
		if !current {
			return
		}
		if _, err := r.flushMemory(timerCtx, previousResponseID, "interval"); err != nil {
			r.log("memory_idle_flush_skipped", map[string]any{"error": err.Error()})
		}
	}()
}

func (r *Runner) runMemoryFlush(ctx context.Context, streamer ResponseStreamer, previousResponseID, previous string, count uint64, trigger string) (MemoryFlushResult, error) {
	prompt := memoryFlushPrompt
	if count > 0 && strings.TrimSpace(previous) != "" {
		prompt += "\n\nExtract only information that is new since this previous flush:\n\n" + previous
	}
	model := strings.TrimSpace(r.MemoryConfig.Flush.Model)
	if model == "" {
		model = r.Model
	}
	r.log("memory_flush_started", map[string]any{"trigger": trigger, "model": model})
	result, err := streamer.StreamResponse(ctx, api.ResponseRequest{
		Model: model, Instructions: prompt,
		Input:              []api.InputItem{{Type: "message", Role: "user", Content: "Now write the memory summary as described in the system prompt."}},
		PreviousResponseID: previousResponseID, Stream: true,
	}, nil)
	outcome, path, accepted := "error", "", ""
	if err == nil {
		accepted, outcome = processMemoryFlushResponse(result.Text, r.MemoryConfig.Flush.MaxWriteChars)
		if outcome == "accepted" {
			var written bool
			path, written, err = r.Memory.Write(trigger, accepted)
			switch {
			case err != nil:
				outcome = "error"
			case written:
				outcome = "written"
			default:
				outcome = "duplicate"
			}
		}
	}
	r.memoryMu.Lock()
	done := r.memoryFlushDone
	r.memoryFlushCount++
	if outcome == "written" {
		r.memoryLastFlush = accepted
	}
	r.memoryMu.Unlock()
	data := map[string]any{"trigger": trigger, "outcome": outcome}
	if path != "" {
		data["path"] = path
	}
	if err != nil {
		data["error"] = err.Error()
	}
	r.log("memory_flush_completed", data)
	r.memoryMu.Lock()
	r.memoryFlushRunning = false
	if r.memoryFlushDone == done {
		r.memoryFlushDone = nil
	}
	if done != nil {
		close(done)
	}
	r.memoryMu.Unlock()
	return MemoryFlushResult{Outcome: outcome, Path: path}, err
}

func (r *Runner) WaitMemory(ctx context.Context) error {
	r.memoryMu.Lock()
	cancel, idleDone := r.memoryIdleCancel, r.memoryIdleDone
	r.memoryIdleCancel = nil
	r.memoryMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if err := waitMemoryTask(ctx, idleDone); err != nil {
		return err
	}
	r.memoryMu.Lock()
	flushDone := r.memoryFlushDone
	r.memoryMu.Unlock()
	return waitMemoryTask(ctx, flushDone)
}

func (r *Runner) CloseMemory(ctx context.Context) error {
	if err := r.WaitMemory(ctx); err != nil {
		return err
	}
	return r.saveSessionMemory()
}

func (r *Runner) saveSessionMemory() error {
	if r.Memory == nil || !r.MemoryConfig.Enabled || !r.MemoryConfig.SaveOnEnd || strings.TrimSpace(r.SessionPath) == "" {
		return nil
	}
	r.memoryMu.Lock()
	if r.memorySessionSaved {
		r.memoryMu.Unlock()
		return nil
	}
	r.memorySessionSaved = true
	r.memoryMu.Unlock()
	events, err := session.Events(r.SessionPath, "user_prompt", "model_response", "tool_result")
	if err != nil {
		r.log("memory_session_end", map[string]any{"outcome": "error", "error": err.Error()})
		return err
	}
	queries := make([]string, 0, 5)
	totalBytes, userCount, assistantCount, toolCount := 0, 0, 0, 0
	for _, event := range events {
		switch event.Kind {
		case "user_prompt":
			data, _ := event.Data.(map[string]any)
			text, _ := data["text"].(string)
			synthetic, _ := data["synthetic"].(bool)
			text = strings.TrimSpace(text)
			if synthetic || text == "" || text == "__auto_continue__" {
				continue
			}
			userCount++
			totalBytes += len(text)
			if len(queries) < 5 {
				queries = append(queries, text)
			}
		case "model_response":
			assistantCount++
		case "tool_result":
			toolCount++
		}
	}
	if userCount < 3 || totalBytes < 50 {
		r.log("memory_session_end", map[string]any{"outcome": "skipped", "user_messages": userCount, "query_bytes": totalBytes})
		return nil
	}
	var summary strings.Builder
	summary.WriteString("## Session Summary\n\n")
	fmt.Fprintf(&summary, "- **Messages:** %d user, %d assistant, %d tool results\n", userCount, assistantCount, toolCount)
	fmt.Fprintf(&summary, "- **Date:** %s\n\n## Topics Discussed\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	for index, query := range queries {
		runes := []rune(query)
		if len(runes) > 100 {
			runes = runes[:100]
		}
		fmt.Fprintf(&summary, "%d. %s\n", index+1, string(runes))
	}
	path, written, err := r.Memory.Write("session_end", summary.String())
	outcome := "duplicate"
	if err != nil {
		outcome = "error"
	} else if written {
		outcome = "written"
	}
	data := map[string]any{"outcome": outcome, "user_messages": userCount, "query_bytes": totalBytes}
	if path != "" {
		data["path"] = path
	}
	if err != nil {
		data["error"] = err.Error()
	}
	r.log("memory_session_end", data)
	return err
}

func waitMemoryTask(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func processMemoryFlushResponse(value string, maxChars int) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" || isNoReply(value) {
		return "", "nothing_to_store"
	}
	runes := []rune(value)
	if maxChars > 0 && len(runes) > maxChars {
		value = string(runes[:maxChars])
	}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			return value, "accepted"
		}
	}
	return "", "rejected"
}

func isNoReply(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "", "-", "", " ", "").Replace(value)
	return value == "noreply"
}

func (r *Runner) resetMemoryFlushCycle() {
	r.memoryMu.Lock()
	r.memoryFlushArmed = false
	r.memoryMu.Unlock()
}
