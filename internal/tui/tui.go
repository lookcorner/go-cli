package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

const (
	mouseWheelScrollLines     = 3
	questionDoubleClickWindow = 500 * time.Millisecond
	maxHistorySearchResults   = 100
	historySearchPageSize     = 8
)

type textEvent struct{ text string }
type statusEvent struct{ text string }
type approvalEvent struct {
	action string
	detail string
	reply  chan bool
}
type questionEvent struct {
	request tools.UserQuestionRequest
	reply   chan tools.UserQuestionResponse
}
type turnDoneEvent struct {
	result agent.Result
	err    error
}
type compactDoneEvent struct{ err error }
type memoryFlushDoneEvent struct {
	result agent.MemoryFlushResult
	err    error
}
type memoryFilesDoneEvent struct {
	files []memory.FileInfo
	err   error
}
type memoryToggleDoneEvent struct {
	message string
	err     error
}
type memoryNoteEnhancedEvent struct {
	nonce uint64
	text  string
}
type memoryNoteSavedEvent struct {
	path string
	err  error
}
type memoryDreamDoneEvent struct {
	result memory.DreamResult
	err    error
}
type scheduledFiredEvent struct{ event tools.ScheduledTaskFired }
type wakeCancelledEvent struct{ id string }
type mouseScrollEvent struct{ lines int }
type mouseClickEvent struct {
	action string
	option int
}
type planModeEvent struct{ active bool }
type planReviewEvent struct {
	event tools.PlanModeEvent
	reply chan tools.PlanModeDecision
}

type Bridge struct {
	ctx           context.Context
	cancel        context.CancelFunc
	mode          tools.PermissionMode
	events        chan tea.Msg
	once          sync.Once
	interactionMu sync.Mutex
}

func NewBridge(parent context.Context, mode tools.PermissionMode) *Bridge {
	ctx, cancel := context.WithCancel(parent)
	return &Bridge{ctx: ctx, cancel: cancel, mode: mode, events: make(chan tea.Msg, 1024)}
}

func (b *Bridge) Close() { b.once.Do(b.cancel) }

func (b *Bridge) ScheduledTaskCreated(tools.ScheduledTaskCreated) {}
func (b *Bridge) ScheduledTaskRemoved(string)                     {}
func (b *Bridge) ScheduledTaskFired(event tools.ScheduledTaskFired) {
	select {
	case b.events <- scheduledFiredEvent{event: event}:
	case <-b.ctx.Done():
	}
}

func (b *Bridge) TrackWake(string) {}

func (b *Bridge) QueueWake(id, prompt string) bool {
	select {
	case b.events <- scheduledFiredEvent{event: tools.ScheduledTaskFired{TaskID: id, Prompt: prompt}}:
		return true
	case <-b.ctx.Done():
		return false
	}
}

func (b *Bridge) CancelWake(id string) { b.send(wakeCancelledEvent{id: id}) }

func (b *Bridge) PlanModeEntered(tools.PlanModeEvent) { b.send(planModeEvent{active: true}) }
func (b *Bridge) PlanModeExited(tools.PlanModeEvent)  { b.send(planModeEvent{}) }

func (b *Bridge) ApprovePlanModeExit(ctx context.Context, event tools.PlanModeEvent) (tools.PlanModeDecision, error) {
	b.interactionMu.Lock()
	defer b.interactionMu.Unlock()
	reply := make(chan tools.PlanModeDecision, 1)
	select {
	case b.events <- planReviewEvent{event: event, reply: reply}:
	case <-ctx.Done():
		return tools.PlanModeDecision{}, ctx.Err()
	case <-b.ctx.Done():
		return tools.PlanModeDecision{}, b.ctx.Err()
	}
	select {
	case decision := <-reply:
		return decision, nil
	case <-ctx.Done():
		return tools.PlanModeDecision{}, ctx.Err()
	case <-b.ctx.Done():
		return tools.PlanModeDecision{}, b.ctx.Err()
	}
}

func (b *Bridge) send(message tea.Msg) {
	select {
	case b.events <- message:
	case <-b.ctx.Done():
	}
}

func (b *Bridge) TextWriter() io.Writer   { return bridgeWriter{bridge: b, status: false} }
func (b *Bridge) StatusWriter() io.Writer { return bridgeWriter{bridge: b, status: true} }

func (b *Bridge) Approve(ctx context.Context, action, detail string) error {
	switch b.mode {
	case tools.PermissionAuto:
		return nil
	case tools.PermissionDeny:
		return fmt.Errorf("permission denied for %s", action)
	case tools.PermissionPrompt:
		return b.prompt(ctx, action, detail)
	default:
		return fmt.Errorf("unknown permission mode %q", b.mode)
	}
}

func (b *Bridge) AskUserQuestion(ctx context.Context, request tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	b.interactionMu.Lock()
	defer b.interactionMu.Unlock()
	reply := make(chan tools.UserQuestionResponse, 1)
	select {
	case b.events <- questionEvent{request: request, reply: reply}:
	case <-ctx.Done():
		return tools.UserQuestionResponse{}, ctx.Err()
	case <-b.ctx.Done():
		return tools.UserQuestionResponse{}, b.ctx.Err()
	}
	select {
	case response := <-reply:
		return response, nil
	case <-ctx.Done():
		return tools.UserQuestionResponse{}, ctx.Err()
	case <-b.ctx.Done():
		return tools.UserQuestionResponse{}, b.ctx.Err()
	}
}

type promptApprover struct{ bridge *Bridge }

func PromptApprover(bridge *Bridge) tools.Approver { return promptApprover{bridge: bridge} }

func (a promptApprover) Approve(ctx context.Context, action, detail string) error {
	return a.bridge.prompt(ctx, action, detail)
}

func (b *Bridge) prompt(ctx context.Context, action, detail string) error {
	b.interactionMu.Lock()
	defer b.interactionMu.Unlock()
	reply := make(chan bool, 1)
	request := approvalEvent{action: action, detail: detail, reply: reply}
	select {
	case b.events <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return b.ctx.Err()
	}
	select {
	case allowed := <-reply:
		if allowed {
			return nil
		}
		return fmt.Errorf("permission denied for %s", action)
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return b.ctx.Err()
	}
}

type bridgeWriter struct {
	bridge *Bridge
	status bool
}

func (w bridgeWriter) Write(data []byte) (int, error) {
	text := string(append([]byte(nil), data...))
	var event tea.Msg = textEvent{text: text}
	if w.status {
		event = statusEvent{text: text}
	}
	select {
	case w.bridge.events <- event:
		return len(data), nil
	case <-w.bridge.ctx.Done():
		return 0, w.bridge.ctx.Err()
	}
}

type model struct {
	ctx           context.Context
	runner        *agent.Runner
	bridge        *Bridge
	workspace     string
	modelName     string
	previousID    string
	transcript    strings.Builder
	input         []rune
	cursor        int
	history       []string
	historyIndex  int
	historyActive bool
	historySearch *historySearchState
	width         int
	height        int
	scroll        int
	running       bool
	status        string
	approval      *approvalEvent
	question      *questionState
	planMode      bool
	planReview    *planReviewState
	remember      *rememberReviewState
	rememberInput bool
	rememberNonce uint64
	turnCancel    context.CancelFunc
	initial       string
	scheduled     []tools.ScheduledTaskFired
	activeTask    string
	questionClick struct {
		option int
		at     time.Time
	}
}

type historySearchState struct {
	results  []string
	selected int
}

type planReviewState struct {
	event   planReviewEvent
	editing bool
}

type rememberReviewState struct {
	raw          string
	enhanced     string
	enhanceDone  bool
	showEnhanced bool
	nonce        uint64
}

type questionState struct {
	event       questionEvent
	index       int
	answers     map[string][]string
	annotations map[string]tools.UserQuestionAnnotation
	partial     map[string]string
}

func Run(ctx context.Context, runner *agent.Runner, bridge *Bridge, initialPrompt, previousID, initialTranscript, workspace, modelName string) error {
	defer bridge.Close()
	runner.TextOutput = bridge.TextWriter()
	runner.StatusOutput = bridge.StatusWriter()
	m := &model{
		ctx: ctx, runner: runner, bridge: bridge, workspace: workspace,
		modelName: modelName, previousID: previousID, width: 80, height: 24,
		status: "ready", initial: strings.TrimSpace(initialPrompt), historyIndex: -1,
		history: loadPromptHistory(runner, workspace),
	}
	if runner.Tools != nil {
		m.planMode = runner.Tools.PlanModeActive()
	}
	m.transcript.WriteString(strings.TrimSpace(initialTranscript))
	program := tea.NewProgram(m, tea.WithContext(ctx))
	_, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *model) Init() tea.Cmd {
	wait := waitForBridge(m.bridge)
	if m.initial == "" {
		return wait
	}
	prompt := m.initial
	m.initial = ""
	prompt, _ = tools.ExpandLoopCommand(prompt)
	m.rememberPrompt(prompt)
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.running = true
	m.beginTurn(prompt)
	return tea.Batch(wait, runTurn(turnCtx, m.runner, prompt, m.previousID))
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 20)
		m.height = max(msg.Height, 10)
	case textEvent:
		before := 0
		if m.scroll > 0 {
			before = len(renderMarkdown(m.transcript.String(), max(m.width, 20)))
		}
		m.transcript.WriteString(msg.text)
		if m.scroll > 0 {
			after := len(renderMarkdown(m.transcript.String(), max(m.width, 20)))
			m.scroll += max(after-before, 0)
		}
		return m, waitForBridge(m.bridge)
	case mouseScrollEvent:
		m.scroll = max(0, m.scroll+msg.lines)
		return m, nil
	case mouseClickEvent:
		switch msg.action {
		case "approve", "deny":
			if m.approval != nil {
				allowed := msg.action == "approve"
				m.approval.reply <- allowed
				m.approval = nil
				if allowed {
					m.status = "approved"
				} else {
					m.status = "denied"
				}
			}
		case "plan_approve", "plan_revise", "plan_abandon":
			if m.planReview != nil && !m.planReview.editing {
				switch msg.action {
				case "plan_approve":
					m.finishPlanReview(tools.PlanModeDecision{Outcome: "approved"})
				case "plan_revise":
					m.planReview.editing = true
					m.clearInput()
					m.status = "request plan changes"
				case "plan_abandon":
					m.finishPlanReview(tools.PlanModeDecision{Outcome: "abandoned"})
				}
			}
		case "question_option":
			if m.question != nil {
				now := time.Now()
				double := msg.option == m.questionClick.option && !m.questionClick.at.IsZero() && now.Sub(m.questionClick.at) < questionDoubleClickWindow
				m.selectQuestionOption(msg.option, !double)
				if double {
					m.questionClick.at = time.Time{}
					m.submitQuestion()
				} else {
					m.questionClick.option, m.questionClick.at = msg.option, now
				}
			}
		}
		return m, nil
	case statusEvent:
		m.status = cleanStatus(msg.text)
		return m, waitForBridge(m.bridge)
	case approvalEvent:
		m.approval = &msg
		m.status = "approval required"
		return m, waitForBridge(m.bridge)
	case questionEvent:
		m.question = &questionState{
			event: msg, answers: make(map[string][]string, len(msg.request.Questions)),
			annotations: make(map[string]tools.UserQuestionAnnotation), partial: make(map[string]string, len(msg.request.Questions)),
		}
		m.questionClick.at = time.Time{}
		m.clearInput()
		m.status = fmt.Sprintf("question 1/%d", len(msg.request.Questions))
		return m, waitForBridge(m.bridge)
	case planModeEvent:
		m.planMode = msg.active
		if !msg.active {
			m.planReview = nil
		}
		return m, waitForBridge(m.bridge)
	case planReviewEvent:
		m.planMode = true
		m.planReview = &planReviewState{event: msg}
		m.clearInput()
		m.status = "review implementation plan"
		return m, waitForBridge(m.bridge)
	case turnDoneEvent:
		m.running = false
		m.turnCancel = nil
		m.transcript.WriteString("\n")
		if msg.err != nil {
			m.status = "turn failed: " + msg.err.Error()
			m.transcript.WriteString("\n[error] " + msg.err.Error() + "\n")
		} else {
			m.previousID = msg.result.ResponseID
			m.status = fmt.Sprintf("ready · %d step(s)", msg.result.Steps)
			if msg.result.InputTokens > 0 && msg.result.ContextWindow > 0 {
				percent := msg.result.InputTokens * 100 / msg.result.ContextWindow
				m.status += fmt.Sprintf(" · context %d/%d (%d%%)", msg.result.InputTokens, msg.result.ContextWindow, percent)
			}
		}
		m.activeTask = ""
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case scheduledFiredEvent:
		if msg.event.TaskID != m.activeTask {
			duplicate := false
			for _, event := range m.scheduled {
				duplicate = duplicate || event.TaskID == msg.event.TaskID
			}
			if !duplicate {
				m.scheduled = append(m.scheduled, msg.event)
			}
		}
		if command := m.startScheduled(); command != nil {
			return m, tea.Batch(waitForBridge(m.bridge), command)
		}
		return m, waitForBridge(m.bridge)
	case wakeCancelledEvent:
		kept := m.scheduled[:0]
		for _, event := range m.scheduled {
			if event.TaskID != msg.id {
				kept = append(kept, event)
			}
		}
		m.scheduled = kept
		return m, waitForBridge(m.bridge)
	case compactDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "compact failed: " + msg.err.Error()
		} else {
			m.previousID = ""
			m.status = "context compacted"
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case memoryFlushDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "memory flush failed: " + msg.err.Error()
		} else {
			m.status = "memory flush: " + msg.result.Outcome
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case memoryFilesDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "memory list failed: " + msg.err.Error()
		} else {
			m.status = fmt.Sprintf("memory files: %d", len(msg.files))
			for _, file := range msg.files {
				fmt.Fprintf(&m.transcript, "\n[memory] %s %d %s\n", file.Source, file.SizeBytes, file.Path)
			}
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case memoryToggleDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "memory toggle failed: " + msg.err.Error()
		} else {
			m.status = msg.message
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case memoryNoteEnhancedEvent:
		if m.remember != nil && m.remember.nonce == msg.nonce {
			m.remember.enhanced = msg.text
			m.remember.enhanceDone = true
			m.status = "memory note ready"
		}
	case memoryNoteSavedEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "memory note save failed: " + msg.err.Error()
		} else {
			m.status = "memory saved"
			fmt.Fprintf(&m.transcript, "\n[memory] Memory saved to %s\n", msg.path)
		}
	case memoryDreamDoneEvent:
		m.running, m.turnCancel = false, nil
		if msg.err != nil {
			m.status = "memory dream failed: " + msg.err.Error()
		} else {
			m.status = "memory dream: " + msg.result.Outcome
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	stroke := msg.Keystroke()
	if m.approval != nil {
		switch strings.ToLower(key.Text) {
		case "y":
			m.approval.reply <- true
			m.approval = nil
			m.status = "approved"
		case "n":
			m.approval.reply <- false
			m.approval = nil
			m.status = "denied"
		default:
			if stroke == "esc" || stroke == "ctrl+c" {
				m.approval.reply <- false
				m.approval = nil
				m.status = "denied"
			}
		}
		return m, nil
	}
	if m.planReview != nil {
		return m.handlePlanReviewKey(msg)
	}
	if m.remember != nil {
		return m.handleRememberReviewKey(msg)
	}
	if m.question != nil {
		return m.handleQuestionKey(msg)
	}
	if m.historySearch != nil {
		return m.handleHistorySearchKey(msg)
	}
	switch stroke {
	case "ctrl+c":
		if m.running && m.turnCancel != nil {
			m.turnCancel()
			m.status = "cancelling turn"
			return m, nil
		}
		return m, tea.Quit
	case "ctrl+q":
		if m.turnCancel != nil {
			m.turnCancel()
		}
		return m, tea.Quit
	case "pgup", "ctrl+up":
		m.scroll += max(m.contentHeight()/2, 1)
		return m, nil
	case "pgdown", "ctrl+down":
		m.scroll -= max(m.contentHeight()/2, 1)
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil
	}
	if m.rememberInput && key.Code == tea.KeyEsc {
		m.rememberInput = false
		m.clearInput()
		m.status = "memory note cancelled"
		return m, nil
	}
	if m.running {
		return m, nil
	}
	if !m.rememberInput {
		switch key.Code {
		case tea.KeyUp:
			m.browseHistory(-1)
			return m, nil
		case tea.KeyDown:
			m.browseHistory(1)
			return m, nil
		case tea.KeyEsc:
			if m.historyActive {
				m.closeHistory()
				return m, nil
			}
		}
	}
	if m.historyActive {
		m.historyActive = false
		m.historyIndex = -1
	}
	if stroke == "shift+tab" {
		if m.runner.Tools == nil {
			m.status = "plan mode unavailable"
			return m, nil
		}
		next := !m.planMode
		if err := m.runner.Tools.SetPlanMode(next); err != nil {
			m.status = "plan mode failed: " + err.Error()
			return m, nil
		}
		m.planMode = next
		if next {
			m.status = "plan mode"
		} else {
			m.status = "ready"
		}
		return m, nil
	}
	switch key.Code {
	case tea.KeyEnter:
		prompt := strings.TrimSpace(string(m.input))
		if prompt == "" {
			return m, nil
		}
		m.clearInput()
		if m.rememberInput {
			m.rememberInput = false
			return m.startRememberReview(prompt)
		}
		if note, ok := tools.ParseRememberCommand(prompt); ok {
			if note == "" {
				m.rememberInput = true
				m.status = "remember mode"
				return m, nil
			}
			return m.startRememberReview(note)
		}
		if prompt == "/history" {
			m.openHistorySearch()
			return m, nil
		}
		m.running = true
		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		if action, ok := tools.ParseMemoryCommand(prompt); ok {
			if action == "browse" {
				m.status = "listing memory"
				return m, runMemoryFiles(m.runner)
			}
			m.status = "updating memory"
			return m, runMemoryToggle(turnCtx, m.runner, action == "enable")
		}
		if prompt == "/compact" {
			m.status = "compacting context"
			return m, runCompact(turnCtx, m.runner, m.previousID)
		}
		if prompt == "/flush" {
			m.status = "flushing memory"
			return m, runMemoryFlush(turnCtx, m.runner, m.previousID)
		}
		if prompt == "/dream" {
			m.status = "consolidating memory"
			return m, runMemoryDream(turnCtx, m.runner)
		}
		prompt, _ = tools.ExpandLoopCommand(prompt)
		m.rememberPrompt(prompt)
		m.beginTurn(prompt)
		return m, runTurn(turnCtx, m.runner, prompt, m.previousID)
	}
	m.editInput(msg)
	return m, nil
}

func loadPromptHistory(runner *agent.Runner, workspace string) []string {
	if runner == nil || strings.TrimSpace(runner.SessionPath) == "" {
		return nil
	}
	items, err := session.PromptHistory(filepath.Dir(runner.SessionPath), workspace, "", true)
	if err != nil {
		return nil
	}
	return dedupePromptHistory(items)
}

func dedupePromptHistory(items []string) []string {
	result := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}

func (m *model) rememberPrompt(prompt string) {
	key := strings.TrimSpace(prompt)
	if key == "" {
		return
	}
	history := make([]string, 1, min(len(m.history)+1, session.MaxPromptHistoryEntries))
	history[0] = prompt
	for _, item := range m.history {
		if strings.TrimSpace(item) != key && len(history) < cap(history) {
			history = append(history, item)
		}
	}
	m.history = history
	m.historyActive = false
	m.historyIndex = -1
}

func (m *model) browseHistory(direction int) {
	if !m.historyActive {
		if direction > 0 || len(m.input) > 0 || len(m.history) == 0 {
			return
		}
		m.historyActive = true
		m.historyIndex = 0
	} else if direction < 0 {
		m.historyIndex = min(m.historyIndex+1, len(m.history)-1)
	} else if m.historyIndex == 0 {
		m.closeHistory()
		return
	} else {
		m.historyIndex--
	}
	m.input = []rune(m.history[m.historyIndex])
	m.cursor = len(m.input)
}

func (m *model) closeHistory() {
	m.historyActive = false
	m.historyIndex = -1
	m.clearInput()
}

func (m *model) openHistorySearch() {
	m.clearInput()
	m.scroll = 0
	m.historySearch = &historySearchState{}
	m.refreshHistorySearch()
	m.status = "search prompt history"
}

func (m *model) handleHistorySearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, stroke := msg.Key(), msg.Keystroke()
	switch {
	case key.Code == tea.KeyEsc || stroke == "ctrl+c":
		m.historySearch = nil
		m.clearInput()
		m.status = "ready"
	case key.Code == tea.KeyEnter || key.Code == tea.KeyTab:
		if selected := m.selectedHistorySearchResult(); selected != "" {
			m.setInput(selected)
		} else {
			m.clearInput()
		}
		m.historySearch = nil
		m.status = "ready"
	case key.Code == tea.KeyUp || stroke == "ctrl+p" || stroke == "ctrl+k":
		m.historySearch.selected = max(0, m.historySearch.selected-1)
	case key.Code == tea.KeyDown || stroke == "ctrl+n" || stroke == "ctrl+j":
		m.historySearch.selected = min(len(m.historySearch.results)-1, m.historySearch.selected+1)
	case stroke == "pgup" || stroke == "ctrl+u":
		m.historySearch.selected = max(0, m.historySearch.selected-historySearchPageSize)
	case stroke == "pgdown" || stroke == "ctrl+d":
		m.historySearch.selected = min(len(m.historySearch.results)-1, m.historySearch.selected+historySearchPageSize)
	default:
		m.editInput(msg)
		m.refreshHistorySearch()
	}
	return m, nil
}

func (m *model) selectedHistorySearchResult() string {
	if m.historySearch == nil || m.historySearch.selected < 0 || m.historySearch.selected >= len(m.historySearch.results) {
		return ""
	}
	return m.historySearch.results[m.historySearch.selected]
}

func (m *model) refreshHistorySearch() {
	if m.historySearch == nil {
		return
	}
	m.historySearch.results = filterPromptHistory(m.history, string(m.input))
	m.historySearch.selected = len(m.historySearch.results) - 1
}

func filterPromptHistory(history []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		limit := min(len(history), maxHistorySearchResults)
		results := append([]string(nil), history[:limit]...)
		slices.Reverse(results)
		return results
	}
	type match struct {
		text  string
		score int
		index int
	}
	matches := make([]match, 0, len(history))
	for index, item := range history {
		if score, ok := fuzzyPromptScore(item, query); ok {
			matches = append(matches, match{text: item, score: score, index: index})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].index < matches[j].index
	})
	if len(matches) > maxHistorySearchResults {
		matches = matches[:maxHistorySearchResults]
	}
	results := make([]string, len(matches))
	for index := range matches {
		results[len(matches)-1-index] = matches[index].text
	}
	return results
}

func fuzzyPromptScore(text, query string) (int, bool) {
	textRunes := []rune(strings.ToLower(text))
	queryRunes := []rune(strings.ToLower(query))
	position, previous, score := 0, -2, 0
	for _, wanted := range queryRunes {
		found := -1
		for index := position; index < len(textRunes); index++ {
			if textRunes[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		score += 10 - found
		if found == previous+1 {
			score += 20
		}
		if found == 0 || textRunes[found-1] == ' ' || textRunes[found-1] == '/' || textRunes[found-1] == '-' || textRunes[found-1] == '_' {
			score += 12
		}
		previous, position = found, found+1
	}
	return score, true
}

func (m *model) startRememberReview(raw string) (tea.Model, tea.Cmd) {
	m.rememberNonce++
	nonce := m.rememberNonce
	m.remember = &rememberReviewState{raw: raw, nonce: nonce}
	m.scroll = 0
	m.status = "enhancing memory note"
	return m, runMemoryNoteEnhance(m.ctx, m.runner, raw, nonce)
}

func (m *model) handleRememberReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	stroke := msg.Keystroke()
	switch {
	case key.Code == tea.KeyEsc:
		m.remember = nil
		m.status = "memory note cancelled"
	case key.Code == tea.KeyTab:
		if m.remember.enhanced != "" {
			m.remember.showEnhanced = !m.remember.showEnhanced
			m.scroll = 0
		}
	case stroke == "up" || stroke == "pgup":
		m.scroll += max(m.contentHeight()/2, 1)
	case stroke == "down" || stroke == "pgdown":
		m.scroll = max(0, m.scroll-max(m.contentHeight()/2, 1))
	case key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y"):
		content := m.remember.raw
		if m.remember.showEnhanced && m.remember.enhanced != "" {
			content = m.remember.enhanced
		}
		m.remember = nil
		m.running = true
		m.status = "saving memory note"
		return m, runMemoryNoteSave(m.runner, content)
	}
	return m, nil
}

func (m *model) handlePlanReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, stroke := msg.Key(), msg.Keystroke()
	if !m.planReview.editing {
		switch strings.ToLower(key.Text) {
		case "y":
			m.finishPlanReview(tools.PlanModeDecision{Outcome: "approved"})
		case "r":
			m.planReview.editing = true
			m.clearInput()
			m.status = "request plan changes"
		case "a":
			m.finishPlanReview(tools.PlanModeDecision{Outcome: "abandoned"})
		default:
			if stroke == "esc" || stroke == "ctrl+c" {
				m.finishPlanReview(tools.PlanModeDecision{Outcome: "cancelled"})
			}
		}
		return m, nil
	}
	if stroke == "esc" {
		m.planReview.editing = false
		m.clearInput()
		m.status = "review implementation plan"
		return m, nil
	}
	switch key.Code {
	case tea.KeyEnter:
		feedback := strings.TrimSpace(string(m.input))
		if feedback == "" {
			m.status = "plan feedback is required"
			return m, nil
		}
		m.finishPlanReview(tools.PlanModeDecision{Outcome: "cancelled", Feedback: feedback})
	default:
		m.editInput(msg)
	}
	return m, nil
}

func (m *model) finishPlanReview(decision tools.PlanModeDecision) {
	m.planReview.event.reply <- decision
	m.planReview = nil
	m.clearInput()
	m.status = "thinking"
}

func (m *model) handleQuestionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, stroke := msg.Key(), msg.Keystroke()
	if stroke == "esc" || stroke == "ctrl+c" {
		m.finishQuestion(tools.UserQuestionResponse{Outcome: "cancelled"})
		return m, nil
	}
	if m.question.event.request.Mode == "plan" {
		switch stroke {
		case "ctrl+r":
			m.finishQuestion(tools.UserQuestionResponse{Outcome: "chat_about_this", PartialAnswers: m.question.partial})
			return m, nil
		case "ctrl+s":
			m.finishQuestion(tools.UserQuestionResponse{Outcome: "skip_interview", PartialAnswers: m.question.partial})
			return m, nil
		}
	}
	switch key.Code {
	case tea.KeyEnter:
		m.submitQuestion()
	default:
		m.editInput(msg)
	}
	return m, nil
}

func (m *model) selectQuestionOption(index int, toggle bool) {
	question := m.question.event.request.Questions[m.question.index]
	if index < 0 || index >= len(question.Options) {
		return
	}
	value := strconv.Itoa(index + 1)
	parts := strings.Split(string(m.input), ",")
	selected := false
	kept := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == value || strings.EqualFold(part, question.Options[index].Label) {
			selected = true
			continue
		}
		if part != "" {
			kept = append(kept, part)
		}
	}
	if !question.MultiSelect {
		kept = kept[:0]
	}
	if !selected || !toggle {
		kept = append(kept, value)
	}
	m.input = []rune(strings.Join(kept, ", "))
	m.cursor = len(m.input)
}

func (m *model) submitQuestion() {
	question := m.question.event.request.Questions[m.question.index]
	answers, annotation, err := tools.ParseUserQuestionAnswer(question, string(m.input))
	if err != nil {
		m.status = "invalid answer: " + err.Error()
		return
	}
	m.question.answers[question.Question] = answers
	m.question.partial[question.Question] = strings.Join(answers, ", ")
	if annotation.Preview != "" || annotation.Notes != "" {
		m.question.annotations[question.Question] = annotation
	}
	m.question.index++
	m.questionClick.at = time.Time{}
	m.clearInput()
	if m.question.index == len(m.question.event.request.Questions) {
		m.finishQuestion(tools.UserQuestionResponse{Outcome: "accepted", Answers: m.question.answers, Annotations: m.question.annotations})
	} else {
		m.status = fmt.Sprintf("question %d/%d", m.question.index+1, len(m.question.event.request.Questions))
	}
}

func (m *model) finishQuestion(response tools.UserQuestionResponse) {
	m.question.event.reply <- response
	m.question = nil
	m.questionClick.at = time.Time{}
	m.clearInput()
	m.status = "thinking"
}

func (m *model) clearInput() {
	m.input = nil
	m.cursor = 0
}

func (m *model) setInput(value string) {
	m.input = []rune(value)
	m.cursor = len(m.input)
}

func (m *model) editInput(message tea.KeyPressMsg) {
	key, stroke := message.Key(), message.Keystroke()
	m.cursor = min(max(m.cursor, 0), len(m.input))
	switch {
	case key.Code == tea.KeyLeft:
		m.cursor = max(0, m.cursor-1)
	case key.Code == tea.KeyRight:
		m.cursor = min(len(m.input), m.cursor+1)
	case key.Code == tea.KeyHome || stroke == "ctrl+a":
		m.cursor = 0
	case key.Code == tea.KeyEnd || stroke == "ctrl+e":
		m.cursor = len(m.input)
	case key.Code == tea.KeyBackspace && m.cursor > 0:
		copy(m.input[m.cursor-1:], m.input[m.cursor:])
		m.input = m.input[:len(m.input)-1]
		m.cursor--
	case key.Code == tea.KeyDelete && m.cursor < len(m.input):
		copy(m.input[m.cursor:], m.input[m.cursor+1:])
		m.input = m.input[:len(m.input)-1]
	case stroke == "ctrl+u":
		m.clearInput()
	case key.Text != "" && utf8.ValidString(key.Text):
		insert := []rune(key.Text)
		oldLength := len(m.input)
		m.input = append(m.input, insert...)
		copy(m.input[m.cursor+len(insert):], m.input[m.cursor:oldLength])
		copy(m.input[m.cursor:], insert)
		m.cursor += len(insert)
	}
}

func (m *model) beginTurn(prompt string) {
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n")
	}
	m.transcript.WriteString("You\n" + prompt + "\n\nGork\n")
	m.status = "thinking"
	m.scroll = 0
}

func waitForBridge(bridge *Bridge) tea.Cmd {
	return func() tea.Msg {
		select {
		case event := <-bridge.events:
			return event
		case <-bridge.ctx.Done():
			return tea.Quit()
		}
	}
}

func runTurn(ctx context.Context, runner *agent.Runner, prompt, previousID string) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.RunTurn(ctx, prompt, previousID)
		return turnDoneEvent{result: result, err: err}
	}
}

func runSyntheticTurn(ctx context.Context, runner *agent.Runner, prompt, previousID string) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.RunSyntheticTurn(ctx, prompt, previousID)
		return turnDoneEvent{result: result, err: err}
	}
}

func (m *model) startScheduled() tea.Cmd {
	if m.running || len(m.scheduled) == 0 {
		return nil
	}
	event := m.scheduled[0]
	m.scheduled = m.scheduled[1:]
	m.activeTask, m.running = event.TaskID, true
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.beginTurn(event.Prompt)
	m.status = "scheduled task " + event.TaskID
	return runSyntheticTurn(turnCtx, m.runner, event.Prompt, m.previousID)
}

func runCompact(ctx context.Context, runner *agent.Runner, previousID string) tea.Cmd {
	return func() tea.Msg {
		_, err := runner.Compact(ctx, previousID)
		return compactDoneEvent{err: err}
	}
}

func runMemoryFlush(ctx context.Context, runner *agent.Runner, previousID string) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.FlushMemory(ctx, previousID)
		return memoryFlushDoneEvent{result: result, err: err}
	}
}

func runMemoryFiles(runner *agent.Runner) tea.Cmd {
	return func() tea.Msg {
		files, err := runner.ListMemory()
		return memoryFilesDoneEvent{files: files, err: err}
	}
}

func runMemoryToggle(ctx context.Context, runner *agent.Runner, enabled bool) tea.Cmd {
	return func() tea.Msg {
		message, err := runner.SetMemoryEnabled(ctx, enabled)
		return memoryToggleDoneEvent{message: message, err: err}
	}
}

func runMemoryDream(ctx context.Context, runner *agent.Runner) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.DreamMemory(ctx, true)
		return memoryDreamDoneEvent{result: result, err: err}
	}
}

func runMemoryNoteEnhance(ctx context.Context, runner *agent.Runner, raw string, nonce uint64) tea.Cmd {
	return func() tea.Msg {
		return memoryNoteEnhancedEvent{nonce: nonce, text: runner.EnhanceMemoryNote(ctx, raw)}
	}
}

func runMemoryNoteSave(runner *agent.Runner, content string) tea.Cmd {
	return func() tea.Msg {
		path, err := runner.SaveMemoryNote(content)
		return memoryNoteSavedEvent{path: path, err: err}
	}
}

func (m *model) View() tea.View {
	width := max(m.width, 20)
	mode := ""
	if m.planMode {
		mode = "  \x1b[30;43m PLAN \x1b[0m"
	}
	header := fmt.Sprintf("\x1b[1m Gork Go\x1b[0m%s  \x1b[2m%s · %s\x1b[0m", mode, truncate(m.modelName, 24), truncate(m.workspace, max(width-45, 10)))
	header = padRight(truncateANSIUnsafe(header, width), width)
	content := m.transcript.String()
	if m.planReview != nil {
		content = "# Review implementation plan\n\n" + m.planReview.event.event.PlanContent
	} else if m.remember != nil {
		label, note := "Raw", m.remember.raw
		if m.remember.showEnhanced && m.remember.enhanced != "" {
			label, note = "Enhanced", m.remember.enhanced
		}
		content = "# Memory Note\n\n**" + label + "**\n\n" + note
	}
	contentLines := renderMarkdown(content, width)
	if m.historySearch != nil {
		contentLines = m.historySearchLines(width, m.contentHeight())
	}
	visible := sliceFromBottom(contentLines, m.contentHeight(), m.scroll)
	body := strings.Join(visible, "\n")
	for len(visible) < m.contentHeight() {
		body += "\n"
		visible = append(visible, "")
	}

	var footer string
	if m.remember != nil {
		tab := "enhancing..."
		if m.remember.enhanceDone {
			tab = "raw only"
		}
		if m.remember.enhanced != "" {
			tab = "Tab raw/enhanced"
		}
		footer = "\x1b[1;32mMemory note review\x1b[0m\n\x1b[2m" + truncate("Enter/Y save · "+tab+" · Esc cancel", width) + "\x1b[0m"
	} else if m.planReview != nil {
		if m.planReview.editing {
			footer = fmt.Sprintf("\x1b[1;33m%s\x1b[0m\n> %s\n\x1b[2m%s\x1b[0m", truncate("Request plan changes", width), renderInput(m.input, m.cursor, max(width-2, 1)), truncate("Enter send · Esc back · Ctrl-U clear", width))
		} else {
			footer = "\x1b[1;33mPlan review\x1b[0m\n\x1b[2m" + truncate("[Y] approve · [R] request changes · [A] abandon · Esc keep planning", width) + "\x1b[0m"
		}
	} else if m.approval != nil {
		footer = fmt.Sprintf("\x1b[1;33mApprove %s?\x1b[0m %s\n\x1b[2m[y] allow  [n/esc] deny\x1b[0m", m.approval.action, truncate(m.approval.detail, width-20))
	} else if m.question != nil {
		question := m.question.event.request.Questions[m.question.index]
		labels := make([]string, 0, len(question.Options))
		for index, option := range question.Options {
			labels = append(labels, fmt.Sprintf("[%d] %s", index+1, option.Label))
		}
		hint := "Enter answer · Esc cancel"
		if m.question.event.request.Mode == "plan" {
			hint += " · Ctrl-R clarify · Ctrl-S skip"
		}
		footer = fmt.Sprintf("\x1b[1;33m%s\x1b[0m\n%s\n> %s\n\x1b[2m%s\x1b[0m",
			truncate(question.Question, width), truncate(strings.Join(labels, "  ")+"  [Other] type response", width),
			renderInput(m.input, m.cursor, max(width-2, 1)), truncate(hint, width))
	} else if m.rememberInput {
		footer = "\x1b[1;32m# Save a memory note\x1b[0m\n> " + renderInput(m.input, m.cursor, max(width-2, 1)) + "\n\x1b[2mEnter review · Esc cancel\x1b[0m"
	} else if m.historySearch != nil {
		footer = "\x1b[1;32mPrompt history\x1b[0m\n> " + renderInput(m.input, m.cursor, max(width-2, 1)) + "\n\x1b[2mEnter/Tab restore · Esc cancel · Up/Down select\x1b[0m"
	} else {
		input := ""
		if m.running {
			input = "> "
		} else {
			input = "> " + renderInput(m.input, m.cursor, max(width-2, 1))
		}
		footer = input + "\n\x1b[2m" + truncate("Enter send · Shift-Tab mode · PgUp/PgDn scroll · Ctrl-C cancel/quit · Ctrl-Q quit", width) + "\x1b[0m"
	}
	status := "\x1b[2m" + truncate(m.status, width) + "\x1b[0m"
	view := tea.NewView(header + "\n" + body + status + "\n" + footer)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	contentHeight := m.contentHeight()
	view.OnMouse = func(message tea.MouseMsg) tea.Cmd {
		mouse := message.Mouse()
		switch message.(type) {
		case tea.MouseWheelMsg:
			if mouse.Y < 1 || mouse.Y > contentHeight {
				return nil
			}
			lines := 0
			switch mouse.Button {
			case tea.MouseWheelUp:
				lines = mouseWheelScrollLines
			case tea.MouseWheelDown:
				lines = -mouseWheelScrollLines
			default:
				return nil
			}
			return func() tea.Msg { return mouseScrollEvent{lines: lines} }
		case tea.MouseClickMsg:
			if mouse.Button != tea.MouseLeft || mouse.Y < contentHeight+3 {
				return nil
			}
			if event, ok := m.footerClick(mouse.X, mouse.Y, width); ok {
				return func() tea.Msg { return event }
			}
		}
		return nil
	}
	return view
}

func (m *model) footerClick(x, y, width int) (mouseClickEvent, bool) {
	if y != m.contentHeight()+3 {
		return mouseClickEvent{}, false
	}
	if m.approval != nil {
		line := "[y] allow  [n/esc] deny"
		for _, item := range []struct{ label, action string }{{"[y] allow", "approve"}, {"[n/esc] deny", "deny"}} {
			if renderedLabelContains(line, item.label, x, width) {
				return mouseClickEvent{action: item.action}, true
			}
		}
	}
	if m.planReview != nil && !m.planReview.editing {
		line := "[Y] approve · [R] request changes · [A] abandon"
		for _, item := range []struct{ label, action string }{{"[Y] approve", "plan_approve"}, {"[R] request changes", "plan_revise"}, {"[A] abandon", "plan_abandon"}} {
			if renderedLabelContains(line, item.label, x, width) {
				return mouseClickEvent{action: item.action}, true
			}
		}
	}
	if m.question != nil {
		question := m.question.event.request.Questions[m.question.index]
		labels := make([]string, 0, len(question.Options))
		for index, option := range question.Options {
			label := fmt.Sprintf("[%d] %s", index+1, option.Label)
			labels = append(labels, label)
			if renderedLabelContains(strings.Join(labels, "  "), label, x, width) {
				return mouseClickEvent{action: "question_option", option: index}, true
			}
		}
	}
	return mouseClickEvent{}, false
}

func renderedLabelContains(line, label string, x, width int) bool {
	byteStart := strings.Index(line, label)
	if byteStart < 0 {
		return false
	}
	start := len([]rune(line[:byteStart]))
	end := start + len([]rune(label))
	return end <= width && x >= start && x < end
}

func (m *model) contentHeight() int {
	if m.question != nil || m.planReview != nil || m.remember != nil || m.rememberInput {
		return max(m.height-7, 3)
	}
	if m.historySearch != nil {
		return max(m.height-6, 3)
	}
	return max(m.height-5, 3)
}

func (m *model) historySearchLines(width, height int) []string {
	if len(m.historySearch.results) == 0 {
		return []string{"  No matching prompts"}
	}
	end := max(height, m.historySearch.selected+1)
	end = min(end, len(m.historySearch.results))
	start := max(0, end-height)
	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		prefix := "  "
		if index == m.historySearch.selected {
			prefix = "> "
		}
		lines = append(lines, truncate(prefix+strings.ReplaceAll(m.historySearch.results[index], "\n", " "), width))
	}
	return lines
}

func cleanStatus(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[gork]")
	return strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
}

func sliceFromBottom(lines []string, height, scroll int) []string {
	end := len(lines) - scroll
	if end < 0 {
		end = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	start := end - height
	if start < 0 {
		start = 0
	}
	return append([]string(nil), lines[start:end]...)
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func renderInput(input []rune, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	cursor = min(max(cursor, 0), len(input))
	textWidth := width - 1
	start, used := cursor, 0
	for start > 0 {
		charWidth := runeWidth(input[start-1])
		if used+charWidth > textWidth {
			break
		}
		start--
		used += charWidth
	}
	end := cursor
	for end < len(input) {
		charWidth := runeWidth(input[end])
		if used+charWidth > textWidth {
			break
		}
		end++
		used += charWidth
	}
	visible := input[start:end]
	position := cursor - start
	return string(visible[:position]) + "█" + string(visible[position:])
}

func truncateANSIUnsafe(value string, width int) string {
	plain := stripUIANSI(value)
	if len([]rune(plain)) <= width {
		return value
	}
	return truncate(plain, width)
}

func padRight(value string, width int) string {
	plain := stripUIANSI(value)
	if missing := width - len([]rune(plain)); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}

func stripUIANSI(value string) string {
	return strings.NewReplacer("\x1b[1m", "", "\x1b[0m", "", "\x1b[2m", "", "\x1b[30;43m", "").Replace(value)
}
