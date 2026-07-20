package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
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
	return r.runMemoryFlush(ctx, r.compactionStreamer(true), previousResponseID, previous, count, "user_requested")
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
	done := r.memoryFlushDone
	r.memoryMu.Unlock()
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
