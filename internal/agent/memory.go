package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

const memoryFlushPrompt = `You are a memory assistant. Extract useful information from this conversation that would help in future sessions with this user. Write concise markdown with # or ## headers covering substantive decisions and rationale, technical context, debugging techniques, and problems with their solutions. Omit empty sections, user environment preferences, and ephemeral progress. Respond with NO_REPLY when nothing genuinely reusable was learned.`

const memoryRewritePrompt = `You are a memory note formatter. Rewrite the user's note into well-structured markdown suitable for a persistent MEMORY.md file. The note should be:
- Concise but complete
- Start with a descriptive ## heading
- Include enough context to be useful months later
- Reference specific files, decisions, or patterns when relevant
- Use bullet points for multiple items
- Do NOT include timestamps or session IDs
- Do NOT add information that is not present in the original note

Return ONLY the formatted markdown, no explanations.`

const memoryDreamPrompt = `You are performing a dream - a reflective pass over memory files. Synthesize recent session logs into durable, well-organized memories so future sessions orient quickly.

Merge related information, resolve contradictions in favor of recent facts, convert relative dates to absolute dates, and discard greetings, tool noise, message counts, current-state sections, next steps, and preferences already captured globally. Preserve decisions, rationale, architecture, preferences, and problem/solution pairs.

Respond with a single markdown document using ## topic headers. Each topic must be self-contained. Respond with NO_REPLY when nothing is worth persisting.`

type MemoryFlushResult struct {
	Outcome string
	Path    string
}

func (r *Runner) DreamMemory(ctx context.Context, manual bool) (memory.DreamResult, error) {
	if manual {
		r.cancelMemoryDreamCheck()
		r.memoryMu.Lock()
		checkDone := r.memoryDreamCheckDone
		r.memoryMu.Unlock()
		if err := waitMemoryTask(ctx, checkDone); err != nil {
			return memory.DreamResult{}, err
		}
	}
	r.memoryMu.Lock()
	if r.memoryDreamRunning {
		r.memoryMu.Unlock()
		return memory.DreamResult{Outcome: "lock_held"}, nil
	}
	done := make(chan struct{})
	r.memoryDreamRunning, r.memoryDreamDone = true, done
	r.memoryMu.Unlock()
	defer func() {
		r.memoryMu.Lock()
		r.memoryDreamRunning = false
		if r.memoryDreamDone == done {
			r.memoryDreamDone = nil
		}
		close(done)
		r.memoryMu.Unlock()
	}()
	store, cfg := r.memoryState()
	if store == nil || !cfg.Enabled {
		return memory.DreamResult{}, errors.New("memory is not enabled for this session")
	}
	if r.Client == nil {
		return memory.DreamResult{}, errors.New("model client is unavailable")
	}
	input, skipped, err := store.PrepareDream(cfg.Dream, manual)
	if err != nil || skipped.Outcome != "" {
		return skipped, err
	}
	r.log("memory_dream_started", map[string]any{"manual": manual, "sessions": input.Eligible})
	result, err := r.compactionStreamer(false).StreamResponse(ctx, api.ResponseRequest{
		Model: r.Model, Instructions: memoryDreamPrompt,
		Input: []api.InputItem{{Type: "message", Role: "user", Content: input.Content}}, Stream: true,
	}, nil)
	if err != nil {
		r.log("memory_dream_completed", map[string]any{"outcome": "failed", "error": err.Error()})
		return memory.DreamResult{Outcome: "failed", Eligible: input.Eligible}, fmt.Errorf("dream inference failed: %w", err)
	}
	committed, err := store.CommitDream(result.Text, input, cfg.Dream.StaleLockSeconds)
	data := map[string]any{"outcome": committed.Outcome, "eligible": committed.Eligible, "cleaned": committed.Cleaned}
	if committed.Path != "" {
		data["path"] = committed.Path
	}
	if err != nil {
		data["error"] = err.Error()
	}
	r.log("memory_dream_completed", data)
	return committed, err
}

func (r *Runner) cancelMemoryDreamCheck() {
	r.memoryMu.Lock()
	cancel := r.memoryDreamCheckCancel
	r.memoryDreamCheckCancel = nil
	r.memoryMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) scheduleMemoryDreamCheck(ctx context.Context) {
	store, cfg := r.memoryState()
	seconds := cfg.Dream.CheckIntervalSeconds
	if store == nil || !cfg.Enabled || !cfg.Dream.Enabled || seconds == nil || *seconds == 0 || *seconds > uint64((1<<63-1)/int64(time.Second)) {
		return
	}
	timerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.memoryMu.Lock()
	previous := r.memoryDreamCheckCancel
	r.memoryDreamCheckCancel, r.memoryDreamCheckDone = cancel, done
	r.memoryMu.Unlock()
	if previous != nil {
		previous()
	}
	go func() {
		defer func() {
			r.memoryMu.Lock()
			if r.memoryDreamCheckDone == done {
				r.memoryDreamCheckCancel, r.memoryDreamCheckDone = nil, nil
			}
			close(done)
			r.memoryMu.Unlock()
		}()
		ticker := time.NewTicker(time.Duration(*seconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-timerCtx.Done():
				return
			case <-ticker.C:
			}
			_, _ = r.DreamMemory(timerCtx, false)
		}
	}()
}

func (r *Runner) SetMemoryEnabled(ctx context.Context, enabled bool) (string, error) {
	if err := r.WaitMemory(ctx); err != nil {
		return "", err
	}
	r.memoryMu.Lock()
	current := r.Memory != nil && r.MemoryConfig.Enabled
	open := r.OpenMemory
	cfg := r.MemoryConfig
	r.memoryMu.Unlock()
	if current == enabled {
		state := "disabled"
		if enabled {
			state = "enabled"
		}
		return "Memory is already " + state + ".", nil
	}
	if enabled {
		if open == nil {
			return "Memory cannot be enabled (not configured for this session).", nil
		}
		store, err := open()
		if err != nil {
			return "", err
		}
		cfg.Enabled = true
		if err := tools.SetMemoryTools(r.Tools, store, cfg, true); err != nil {
			return "", err
		}
		r.memoryMu.Lock()
		r.Memory, r.MemoryConfig = store, cfg
		r.memoryMu.Unlock()
		return "Memory enabled for this session.", nil
	}
	if err := tools.SetMemoryTools(r.Tools, nil, cfg, false); err != nil {
		return "", err
	}
	r.memoryMu.Lock()
	cfg.Enabled = false
	r.Memory, r.MemoryConfig = nil, cfg
	r.memoryMu.Unlock()
	return "Memory disabled for this session.", nil
}

func (r *Runner) ListMemory() ([]memory.FileInfo, error) {
	store, cfg := r.memoryState()
	if store == nil || !cfg.Enabled {
		return nil, errors.New("memory is not enabled for this session")
	}
	return store.List()
}

func (r *Runner) memoryState() (*memory.Store, memory.Config) {
	r.memoryMu.Lock()
	defer r.memoryMu.Unlock()
	return r.Memory, r.MemoryConfig
}

func (r *Runner) MemoryAvailability() (configured, enabled bool) {
	if r == nil {
		return false, false
	}
	r.memoryMu.Lock()
	defer r.memoryMu.Unlock()
	return r.OpenMemory != nil || r.Memory != nil, r.Memory != nil && r.MemoryConfig.Enabled
}

func (r *Runner) RewriteMemoryNote(ctx context.Context, rawText, contextSummary string) (string, error) {
	const maxInputBytes = 32 << 10
	combined := len(rawText) + len(contextSummary)
	if combined > maxInputBytes {
		return "", fmt.Errorf("memory note input too large (%d bytes, max %d)", combined, maxInputBytes)
	}
	if r.Client == nil {
		return "", errors.New("model client is unavailable")
	}
	temperature := 0.3
	result, err := r.compactionStreamer(false).StreamResponse(ctx, api.ResponseRequest{
		Model: "grok-build", Instructions: memoryRewritePrompt,
		Input:           []api.InputItem{{Type: "message", Role: "user", Content: "Session context:\n" + contextSummary + "\n\nRewrite this note as a memory entry:\n\n" + rawText}},
		MaxOutputTokens: 1024, Temperature: &temperature, Stream: true,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("rewrite inference failed: %w", err)
	}
	if result.Text == "" {
		return "", errors.New("LLM returned empty response")
	}
	return result.Text, nil
}

func (r *Runner) EnhanceMemoryNote(ctx context.Context, rawText string) string {
	result, err := r.RewriteMemoryNote(ctx, rawText, r.memoryNoteContext())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result)
}

func (r *Runner) SaveMemoryNote(content string) (string, error) {
	root, err := memory.DefaultRoot()
	if err != nil {
		return "", err
	}
	return memory.AppendGlobal(root, content)
}

func (r *Runner) memoryNoteContext() string {
	if strings.TrimSpace(r.SessionPath) == "" {
		return ""
	}
	events, err := session.Events(r.SessionPath, "session_metadata", "user_prompt")
	if err != nil {
		return ""
	}
	prompts := make([]string, 0, 5)
	cwd, head := "", ""
	for _, event := range events {
		if event.Kind != "session_metadata" {
			continue
		}
		data, _ := event.Data.(map[string]any)
		cwd, _ = data["cwd"].(string)
		head, _ = data["headCommit"].(string)
	}
	for index := len(events) - 1; index >= 0 && len(prompts) < 5; index-- {
		data, _ := events[index].Data.(map[string]any)
		text, _ := data["text"].(string)
		if synthetic, _ := data["synthetic"].(bool); synthetic {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 200 {
			text = string(runes[:200]) + "..."
		}
		prompts = append(prompts, text)
	}
	if len(prompts) == 0 && cwd == "" && head == "" {
		return ""
	}
	var output strings.Builder
	if cwd != "" {
		output.WriteString("Workspace: " + cwd + "\n")
	}
	if head != "" {
		output.WriteString("HEAD: " + head + "\n")
	}
	if len(prompts) > 0 {
		output.WriteString("Recent user prompts:\n")
		for index := len(prompts) - 1; index >= 0; index-- {
			output.WriteString("- " + prompts[index] + "\n")
		}
	}
	return strings.TrimSpace(output.String())
}

func (r *Runner) injectMemoryContext(content any, query, previousResponseID string) any {
	store, cfg := r.memoryState()
	r.memoryMu.Lock()
	if r.memoryInjected {
		r.memoryMu.Unlock()
		return content
	}
	r.memoryInjected = true
	enabled := previousResponseID == "" && store != nil && cfg.Enabled && cfg.InitialInjection
	r.memoryMu.Unlock()
	if !enabled {
		return content
	}
	search := cfg.Search
	search.MaxResults = 6
	search.MinScore = 0
	if cfg.InitialInjectionMinScore != nil {
		search.MinScore = min(1, max(0, *cfg.InitialInjectionMinScore))
	}
	query = strings.TrimSpace(query)
	if len(query) < 20 || isMemoryGreeting(query) {
		query = "project conventions preferences architecture"
	}
	results, err := store.Search(query, cfg.Index, search)
	if err != nil || len(results) == 0 {
		if err != nil {
			r.log("memory_context_error", map[string]any{"error": err.Error()})
		}
		return content
	}
	value := formatMemorySearchContext(results)
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

func isMemoryGreeting(value string) bool {
	value = strings.ToLower(strings.Trim(value, " \t\r\n.!?,"))
	switch value {
	case "hi", "hey", "hello", "howdy", "continue", "start", "begin", "go", "good morning", "good afternoon", "good evening", "what's up", "whats up", "sup":
		return true
	default:
		return false
	}
}

func formatMemorySearchContext(results []memory.Result) string {
	const maxSnippetChars = 500
	var output strings.Builder
	output.WriteString("<memory-context>\n## Relevant Memory from Past Sessions\n\n")
	for index, result := range results {
		snippet := []rune(result.Snippet)
		truncated := len(snippet) > maxSnippetChars
		if truncated {
			snippet = snippet[:maxSnippetChars]
		}
		fmt.Fprintf(&output, "### Result %d (score: %.2f, source: %s)\n**File:** %s (lines %d-%d)\n", index+1, result.Score, result.Source, result.Path, result.StartLine, result.EndLine)
		if warning := memoryStalenessNote(result); warning != "" {
			output.WriteString(warning + "\n")
		}
		fmt.Fprintf(&output, "```\n%s", string(snippet))
		if truncated {
			output.WriteString("...")
		}
		output.WriteString("\n```\n\n")
	}
	output.WriteString("</memory-context>")
	return output.String()
}

func memoryStalenessNote(result memory.Result) string {
	if result.Source != "session" || result.CreatedAt <= 0 {
		return ""
	}
	age := time.Since(time.Unix(result.CreatedAt, 0))
	if age > 7*24*time.Hour {
		return "**Stale memory:** More than 7 days old; verify before relying on it."
	}
	if age > 24*time.Hour {
		return "**Verification recommended:** This session memory is more than 1 day old."
	}
	return ""
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
	store, memoryConfig := r.memoryState()
	config := memoryConfig.Flush
	if store == nil || !memoryConfig.Enabled || !config.Enabled || previousResponseID == "" || r.ContextWindow <= 0 || r.lastInputTokens <= 0 {
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
	store, memoryConfig := r.memoryState()
	if store == nil || !memoryConfig.Enabled || !memoryConfig.Flush.Enabled {
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
	store, memoryConfig := r.memoryState()
	seconds := memoryConfig.Flush.IdleTimeoutSeconds
	if seconds == nil || store == nil || !memoryConfig.Enabled || !memoryConfig.Flush.Enabled || previousResponseID == "" || *seconds > uint64((1<<63-1)/int64(time.Second)) {
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
	store, memoryConfig := r.memoryState()
	prompt := memoryFlushPrompt
	if count > 0 && strings.TrimSpace(previous) != "" {
		prompt += "\n\nExtract only information that is new since this previous flush:\n\n" + previous
	}
	model := strings.TrimSpace(memoryConfig.Flush.Model)
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
		accepted, outcome = processMemoryFlushResponse(result.Text, memoryConfig.Flush.MaxWriteChars)
		if outcome == "accepted" {
			var written bool
			path, written, err = store.Write(trigger, accepted)
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
	dreamCancel, checkDone, dreamDone := r.memoryDreamCheckCancel, r.memoryDreamCheckDone, r.memoryDreamDone
	r.memoryDreamCheckCancel = nil
	r.memoryMu.Unlock()
	if dreamCancel != nil {
		dreamCancel()
	}
	if err := waitMemoryTask(ctx, flushDone); err != nil {
		return err
	}
	if err := waitMemoryTask(ctx, checkDone); err != nil {
		return err
	}
	return waitMemoryTask(ctx, dreamDone)
}

func (r *Runner) CloseMemory(ctx context.Context) error {
	if err := r.WaitMemory(ctx); err != nil {
		return err
	}
	if err := r.saveSessionMemory(); err != nil {
		return err
	}
	store, cfg := r.memoryState()
	if store == nil || !cfg.Enabled || !cfg.Dream.Enabled {
		return nil
	}
	if _, err := r.DreamMemory(ctx, false); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.log("memory_dream_skipped", map[string]any{"error": err.Error()})
	}
	return nil
}

func (r *Runner) saveSessionMemory() error {
	store, config := r.memoryState()
	if store == nil || !config.Enabled || !config.SaveOnEnd || strings.TrimSpace(r.SessionPath) == "" {
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
	path, written, err := store.Write("session_end", summary.String())
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
