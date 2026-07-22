package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

const (
	mouseWheelScrollLines     = 3
	questionDoubleClickWindow = 500 * time.Millisecond
	textMultiClickWindow      = 300 * time.Millisecond
	selectionHighlightTime    = 150 * time.Millisecond
	maxHistorySearchResults   = 100
	historySearchPageSize     = 8
	maxInputUndoEntries       = 100
	maxPromptInputRows        = 6
	defaultWordSeparators     = "!\"#$%&'()*+,-./:;<=>?@[\\]^`{|}~"
)

var selectionURLPattern = regexp.MustCompile(`(?i)\b(?:https?|ftp|file)://[^\s\x00-\x1f]+`)

type textEvent struct{ text string }
type statusEvent struct{ text string }
type mouseSelectionPhase uint8

const (
	selectionStart mouseSelectionPhase = iota
	selectionMove
	selectionRelease
)

type selectionPoint struct{ line, column int }
type mouseSelectionEvent struct {
	phase mouseSelectionPhase
	point selectionPoint
	lines []string
	at    time.Time
}
type selectionClearEvent struct{ nonce uint64 }
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
type shellDoneEvent struct {
	command string
	output  string
	err     error
}
type copyDoneEvent struct {
	text string
	err  error
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
	modeMu        sync.RWMutex
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
	mode := b.PermissionMode()
	switch mode {
	case tools.PermissionAuto:
		return nil
	case tools.PermissionDeny:
		return fmt.Errorf("permission denied for %s", action)
	case tools.PermissionPrompt:
		return b.prompt(ctx, action, detail)
	default:
		return fmt.Errorf("unknown permission mode %q", mode)
	}
}

func (b *Bridge) PermissionMode() tools.PermissionMode {
	b.modeMu.RLock()
	defer b.modeMu.RUnlock()
	return b.mode
}

func (b *Bridge) SetAlwaysApprove(enabled bool) error {
	b.modeMu.Lock()
	defer b.modeMu.Unlock()
	if enabled && b.mode == tools.PermissionDeny {
		return errors.New("always-approve is disabled by deny mode")
	}
	if enabled {
		b.mode = tools.PermissionAuto
	} else {
		b.mode = tools.PermissionPrompt
	}
	return nil
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
	ctx            context.Context
	runner         *agent.Runner
	bridge         *Bridge
	workspace      string
	modelName      string
	previousID     string
	inputTokens    int
	contextWindow  int
	transcript     strings.Builder
	input          []rune
	cursor         int
	inputUndo      []inputSnapshot
	multiline      bool
	history        []string
	historyIndex   int
	historyActive  bool
	historySearch  *historySearchState
	scrollSearch   *scrollSearchState
	selection      *textSelection
	selectionNonce uint64
	selectionMode  textSelectionMode
	wordSeparators string
	mouseToggle    bool
	mouseReleased  bool
	hyperlinks     bool
	scrollFocused  bool
	selectionClick selectionClickState
	width          int
	height         int
	scroll         int
	running        bool
	status         string
	approval       *approvalEvent
	question       *questionState
	planMode       bool
	planReview     *planReviewState
	remember       *rememberReviewState
	rememberInput  bool
	rememberNonce  uint64
	turnCancel     context.CancelFunc
	initial        string
	scheduled      []tools.ScheduledTaskFired
	activeTask     string
	questionClick  struct {
		option int
		at     time.Time
	}
}

type historySearchState struct {
	results  []string
	selected int
}

type inputSnapshot struct {
	text   []rune
	cursor int
}

type textSelection struct {
	anchor     selectionPoint
	head       selectionPoint
	lines      []string
	moved      bool
	semantic   bool
	nonce      uint64
	table      *tableGeometry
	fromCell   tableCell
	toCell     tableCell
	wholeCell  bool
	wholeTable bool
}

type selectionClickState struct {
	line  int
	count uint8
	at    time.Time
}

type textSelectionMode uint8

const (
	selectionFlash textSelectionMode = iota
	selectionHold
	selectionWord
)

type UIOptions struct {
	Mode                 string
	WordSeparators       *string
	MouseReportingToggle bool
}

func parseTextSelectionMode(value string) textSelectionMode {
	switch value {
	case "hold":
		return selectionHold
	case "word_select":
		return selectionWord
	default:
		return selectionFlash
	}
}

func (m textSelectionMode) holds() bool {
	return m == selectionHold || m == selectionWord
}

func (m textSelectionMode) selectsWord() bool {
	return m == selectionWord
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

func Run(ctx context.Context, runner *agent.Runner, bridge *Bridge, initialPrompt, previousID, initialTranscript, workspace, modelName string, options UIOptions) error {
	defer bridge.Close()
	runner.TextOutput = bridge.TextWriter()
	runner.StatusOutput = bridge.StatusWriter()
	m := &model{
		ctx: ctx, runner: runner, bridge: bridge, workspace: workspace,
		modelName: modelName, previousID: previousID, width: 80, height: 24,
		status: "ready", initial: strings.TrimSpace(initialPrompt), historyIndex: -1,
		history: loadPromptHistory(runner, workspace), selectionMode: parseTextSelectionMode(options.Mode),
		wordSeparators: defaultWordSeparators, mouseToggle: options.MouseReportingToggle,
		hyperlinks: detectTerminalHyperlinks(),
	}
	if options.WordSeparators != nil {
		m.wordSeparators = *options.WordSeparators
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
		m.selection = nil
		m.selectionClick = selectionClickState{}
		m.width = max(msg.Width, 20)
		m.height = max(msg.Height, 10)
		m.refreshScrollSearch()
	case textEvent:
		m.selection = nil
		m.selectionClick = selectionClickState{}
		before := 0
		if m.scroll > 0 {
			before = len(renderMarkdown(m.transcript.String(), max(m.width, 20)))
		}
		m.transcript.WriteString(msg.text)
		if m.scroll > 0 {
			after := len(renderMarkdown(m.transcript.String(), max(m.width, 20)))
			m.scroll += max(after-before, 0)
		}
		m.refreshScrollSearch()
		return m, waitForBridge(m.bridge)
	case mouseScrollEvent:
		m.selection = nil
		m.selectionClick = selectionClickState{}
		m.scroll = max(0, m.scroll+msg.lines)
		return m, nil
	case mouseSelectionEvent:
		switch msg.phase {
		case selectionStart:
			m.scrollFocused = true
			if msg.point.line < 0 || msg.point.line >= len(msg.lines) {
				m.selection = nil
				m.selectionClick = selectionClickState{}
				return m, nil
			}
			m.selectionNonce++
			m.selection = &textSelection{
				anchor: msg.point, head: msg.point, lines: append([]string(nil), msg.lines...), nonce: m.selectionNonce,
			}
			geometry := tableAt(m.selection.lines, msg.point)
			if geometry != nil {
				if cell, ok := geometry.cellAt(msg.point, true); ok {
					m.selection.table, m.selection.fromCell, m.selection.toCell = geometry, cell, cell
				}
			}
			if m.selectionMode.selectsWord() {
				clickedAt := msg.at
				if clickedAt.IsZero() {
					clickedAt = time.Now()
				}
				count := m.countTextClick(clickedAt, msg.point)
				m.selectionClick = selectionClickState{line: msg.point.line, count: count, at: clickedAt}
				line := m.selection.lines[msg.point.line]
				from, to := 0, 0
				switch count {
				case 2:
					m.selection.table = nil
					from, to = semanticDisplayRange(line, msg.point.column, m.wordSeparators)
				case 3:
					if geometry != nil {
						m.selection.table = geometry
						if cell, ok := geometry.cellAt(msg.point, true); ok {
							m.selection.fromCell, m.selection.toCell, m.selection.wholeCell = cell, cell, true
						} else {
							m.selection.fromCell = tableCell{}
							m.selection.toCell = tableCell{row: len(geometry.rows) - 1, column: geometry.columnCount() - 1}
							m.selection.wholeTable = true
						}
						m.selection.moved, m.selection.semantic = true, true
						m.selectionClick = selectionClickState{}
						return m, m.copyTextSelection()
					}
					to = displayWidth(line)
					m.selectionClick = selectionClickState{}
				}
				if to > from {
					m.selection.anchor.column, m.selection.head.column = from, to-1
					m.selection.moved, m.selection.semantic = true, true
					return m, m.copyTextSelection()
				}
			}
		case selectionMove:
			if m.selection != nil {
				if m.selection.semantic {
					return m, nil
				}
				m.selection.moved = m.selection.moved || msg.point != m.selection.anchor
				m.selection.head = msg.point
				if m.selection.table != nil {
					m.selection.toCell = m.selection.table.latchedCell(msg.point, m.selection.toCell)
				}
				if m.selection.moved {
					m.selectionClick = selectionClickState{}
				}
			}
		case selectionRelease:
			if m.selection == nil {
				return m, nil
			}
			if m.selection.semantic {
				return m, nil
			}
			m.selection.moved = m.selection.moved || msg.point != m.selection.anchor
			m.selection.head = msg.point
			if m.selection.table != nil {
				m.selection.toCell = m.selection.table.latchedCell(msg.point, m.selection.toCell)
			}
			if !m.selection.moved {
				m.selection = nil
				return m, nil
			}
			return m, m.copyTextSelection()
		}
		return m, nil
	case selectionClearEvent:
		if m.selection != nil && m.selection.nonce == msg.nonce {
			m.selection = nil
		}
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
			if msg.result.InputTokens > 0 && msg.result.ContextWindow > 0 {
				m.inputTokens = msg.result.InputTokens
				m.contextWindow = msg.result.ContextWindow
			}
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
	case shellDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.output != "" {
			m.transcript.WriteString(strings.TrimRight(msg.output, "\n") + "\n")
		}
		if msg.err != nil {
			m.status = "shell failed: " + msg.err.Error()
			m.transcript.WriteString("[error] " + msg.err.Error() + "\n")
		} else {
			m.status = "shell completed"
		}
		if command := m.startScheduled(); command != nil {
			return m, command
		}
	case copyDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "copy failed: " + msg.err.Error()
		} else if msg.text == "" {
			m.status = "no assistant messages to copy"
		} else {
			m.status = "response copied"
			clipboard := tea.SetClipboard(msg.text)
			if command := m.startScheduled(); command != nil {
				return m, tea.Batch(clipboard, command)
			}
			return m, clipboard
		}
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
	if key.Code == tea.KeyEsc && m.selection != nil && m.scrollSearch == nil {
		m.selection = nil
		m.selectionClick = selectionClickState{}
		return m, nil
	}
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
	if m.scrollSearch != nil && m.handleScrollSearchKey(msg) {
		return m, nil
	}
	if m.scrollFocused && m.handleScrollbackKey(msg) {
		return m, nil
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
	}
	if m.rememberInput && key.Code == tea.KeyEsc {
		m.rememberInput = false
		m.clearInput()
		m.status = "memory note cancelled"
		return m, nil
	}
	if key.Code == tea.KeyTab && key.Mod == 0 {
		m.scrollFocused = true
		m.status = "scrollback focused"
		return m, nil
	}
	if m.running {
		return m, nil
	}
	if !m.rememberInput {
		switch key.Code {
		case tea.KeyUp:
			if !m.historyActive && slices.Contains(m.input, '\n') {
				m.moveInputCursorLine(-1)
			} else {
				m.browseHistory(-1)
			}
			return m, nil
		case tea.KeyDown:
			if !m.historyActive && slices.Contains(m.input, '\n') {
				m.moveInputCursorLine(1)
			} else {
				m.browseHistory(1)
			}
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
	if stroke == "ctrl+m" && !m.rememberInput {
		m.toggleMultiline()
		return m, nil
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
	modifiedEnter := key.Code == tea.KeyEnter && key.Mod&(tea.ModShift|tea.ModAlt) != 0
	if key.Code == tea.KeyEnter && !m.rememberInput {
		if !modifiedEnter && len(m.input) > 0 && m.cursor == len(m.input) && m.input[len(m.input)-1] == '\\' {
			m.saveInputUndo()
			m.input[len(m.input)-1] = '\n'
			return m, nil
		}
		if (!m.multiline && modifiedEnter) || (m.multiline && !modifiedEnter) {
			m.insertInput("\n")
			return m, nil
		}
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
		if prompt == "/find" || strings.HasPrefix(prompt, "/find ") {
			m.openScrollSearch(strings.TrimSpace(strings.TrimPrefix(prompt, "/find")))
			return m, nil
		}
		fields := strings.Fields(prompt)
		switch fields[0] {
		case "/help":
			m.appendSystem("# Commands\n\n`! <command>` `/always-approve` `/compact` `/context` `/copy [N]` `/dream` `/find` `/flush` `/help` `/history` `/loop` `/memory` `/multiline` `/remember` `/session-info`")
			m.status = "commands"
			return m, nil
		case "/always-approve":
			if m.bridge == nil {
				m.status = "always-approve unavailable"
				return m, nil
			}
			enabled := m.bridge.PermissionMode() != tools.PermissionAuto
			if err := m.bridge.SetAlwaysApprove(enabled); err != nil {
				m.status = err.Error()
				return m, nil
			}
			if enabled {
				m.status = "always-approve mode"
			} else {
				m.status = "normal mode"
			}
			return m, nil
		case "/session-info":
			if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
				m.status = "no active session"
				return m, nil
			}
			m.appendSystem(fmt.Sprintf("# Session info\n\n- Session: `%s`\n- Workspace: `%s`\n- Model: `%s`", m.runner.SessionID, m.workspace, m.modelName))
			m.status = "session info"
			return m, nil
		case "/context":
			if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
				m.status = "no active session"
				return m, nil
			}
			if m.inputTokens <= 0 || m.contextWindow <= 0 {
				m.status = "context usage unavailable"
				return m, nil
			}
			percent := m.inputTokens * 100 / m.contextWindow
			m.appendSystem(fmt.Sprintf("# Context usage\n\n%d / %d tokens (%d%%)", m.inputTokens, m.contextWindow, percent))
			m.status = "context usage"
			return m, nil
		}
		if fields[0] == "/multiline" || fields[0] == "/ml" {
			m.toggleMultiline()
			return m, nil
		}
		if fields[0] == "/copy" {
			n, err := copyMessageNumber(strings.TrimSpace(strings.TrimPrefix(prompt, "/copy")))
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			m.running = true
			m.status = "copying response"
			return m, runCopy(m.runner, n)
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
		if strings.HasPrefix(prompt, "!") {
			command := strings.TrimSpace(strings.TrimPrefix(prompt, "!"))
			if command == "" {
				m.running = false
				m.turnCancel = nil
				m.status = "shell command is empty"
				return m, nil
			}
			m.rememberPrompt(prompt)
			if m.transcript.Len() > 0 {
				m.transcript.WriteString("\n")
			}
			fmt.Fprintf(&m.transcript, "[Shell] $ %s\n", command)
			m.status = "running shell command"
			m.scroll = 0
			return m, runShell(turnCtx, m.runner, command)
		}
		prompt, _ = tools.ExpandLoopCommand(prompt)
		m.rememberPrompt(prompt)
		m.beginTurn(prompt)
		return m, runTurn(turnCtx, m.runner, prompt, m.previousID)
	}
	m.editInput(msg)
	return m, nil
}

func (m *model) toggleMultiline() {
	m.multiline = !m.multiline
	if m.multiline {
		m.status = "multiline input"
	} else {
		m.status = "single-line input"
	}
}

func (m *model) appendSystem(text string) {
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n")
	}
	m.transcript.WriteString(strings.TrimSpace(text) + "\n")
	m.scroll = 0
}

func copyMessageNumber(args string) (int, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(args)
	if err != nil || n < 1 {
		return 0, errors.New("usage: /copy [N] where N is 1, 2, 3, ...")
	}
	return n, nil
}

func (m *model) handleScrollbackKey(msg tea.KeyPressMsg) bool {
	key, stroke := msg.Key(), msg.Keystroke()
	if stroke == "ctrl+c" || stroke == "ctrl+q" || stroke == "shift+tab" {
		return false
	}
	switch {
	case key.Mod == 0 && (key.Code == tea.KeyTab || key.Text == " "):
		m.scrollSearch = nil
		m.scrollFocused = false
		m.status = "ready"
	case key.Code == tea.KeyEsc:
		return true
	case stroke == "ctrl+r" && m.mouseToggle:
		m.mouseReleased = !m.mouseReleased
		m.selection = nil
		m.selectionClick = selectionClickState{}
		if m.mouseReleased {
			m.status = "mouse reporting disabled"
		} else {
			m.status = "mouse reporting enabled"
		}
	case stroke == "ctrl+k":
		m.scrollTranscript(1)
	case stroke == "ctrl+j":
		m.scrollTranscript(-1)
	case stroke == "ctrl+u":
		m.scrollTranscript(max(m.contentHeight()/2, 1))
	case stroke == "ctrl+d":
		m.scrollTranscript(-max(m.contentHeight()/2, 1))
	case stroke == "pgup":
		m.scrollTranscript(max(m.contentHeight(), 1))
	case stroke == "pgdown":
		m.scrollTranscript(-max(m.contentHeight(), 1))
	case key.Mod == 0 && key.Text == "g":
		m.scrollTranscript(m.maxTranscriptScroll())
	case key.Mod == 0 && key.Text == "G":
		m.scrollTranscript(-m.scroll)
	case key.Mod == 0 && key.Text == "/":
		m.openScrollSearch("")
	default:
		if key.Mod != 0 || len(key.Text) != 1 || !isASCIILetterOrSlash(key.Text[0]) {
			return true
		}
		m.scrollSearch = nil
		m.scrollFocused = false
		if !m.running {
			m.editInput(msg)
		}
	}
	return true
}

func (m *model) openScrollSearch(query string) {
	if strings.TrimSpace(m.transcript.String()) == "" {
		m.status = "scrollback is empty"
		return
	}
	m.scrollFocused = true
	m.scrollSearch = newScrollSearch()
	m.scrollSearch.query = []rune(query)
	m.scrollSearch.cursor = len(m.scrollSearch.query)
	m.refreshScrollSearch()
}

func (m *model) handleScrollSearchKey(msg tea.KeyPressMsg) bool {
	search := m.scrollSearch
	key, stroke := msg.Key(), msg.Keystroke()
	if key.Code == tea.KeyEsc {
		m.scrollSearch = nil
		m.status = "scrollback focused"
		return true
	}
	if key.Mod == 0 && (key.Code == tea.KeyDown || key.Code == tea.KeyUp) {
		if key.Code == tea.KeyDown {
			search.step(1)
		} else {
			search.step(-1)
		}
		m.revealScrollSearch()
		m.status = search.status()
		return true
	}
	if !search.composing {
		switch {
		case key.Mod == 0 && key.Text == "n":
			search.step(1)
		case key.Text == "N" && (key.Mod == 0 || key.Mod == tea.ModShift):
			search.step(-1)
		case key.Mod == 0 && key.Text == "/":
			search.composing = true
		default:
			return false
		}
		m.revealScrollSearch()
		m.status = search.status()
		return true
	}
	if key.Code == tea.KeyEnter {
		if len(search.query) == 0 {
			m.scrollSearch = nil
			m.status = "scrollback focused"
		} else {
			search.composing = false
			m.revealScrollSearch()
			m.status = search.status()
		}
		return true
	}
	search.cursor = min(max(search.cursor, 0), len(search.query))
	switch {
	case key.Code == tea.KeyLeft:
		search.cursor = max(search.cursor-1, 0)
	case key.Code == tea.KeyRight:
		search.cursor = min(search.cursor+1, len(search.query))
	case key.Code == tea.KeyHome || stroke == "ctrl+a":
		search.cursor = 0
	case key.Code == tea.KeyEnd || stroke == "ctrl+e":
		search.cursor = len(search.query)
	case key.Code == tea.KeyBackspace && search.cursor > 0:
		search.query = append(search.query[:search.cursor-1], search.query[search.cursor:]...)
		search.cursor--
	case key.Code == tea.KeyDelete && search.cursor < len(search.query):
		search.query = append(search.query[:search.cursor], search.query[search.cursor+1:]...)
	case stroke == "ctrl+u":
		search.query = nil
		search.cursor = 0
	case key.Text != "" && (key.Mod == 0 || key.Mod == tea.ModShift) && utf8.ValidString(key.Text):
		insert := []rune(key.Text)
		search.query = append(search.query, insert...)
		copy(search.query[search.cursor+len(insert):], search.query[search.cursor:len(search.query)-len(insert)])
		copy(search.query[search.cursor:], insert)
		search.cursor += len(insert)
	default:
		return true
	}
	m.refreshScrollSearch()
	return true
}

func (m *model) refreshScrollSearch() {
	if m.scrollSearch == nil {
		return
	}
	lines := renderMarkdown(m.transcript.String(), max(m.width, 20))
	for index := range lines {
		lines[index] = stripUIANSI(lines[index])
	}
	m.scrollSearch.update(lines)
	m.revealScrollSearch()
	m.status = m.scrollSearch.status()
}

func (m *model) revealScrollSearch() {
	if m.scrollSearch == nil || m.scrollSearch.current < 0 {
		return
	}
	lines := renderMarkdown(m.transcript.String(), max(m.width, 20))
	target := m.scrollSearch.matches[m.scrollSearch.current].line
	height := m.contentHeight()
	maxStart := max(len(lines)-height, 0)
	start := min(max(target-height/2, 0), maxStart)
	m.scroll = maxStart - start
}

func (m *model) scrollTranscript(lines int) {
	m.selection = nil
	m.selectionClick = selectionClickState{}
	m.scroll = min(max(m.scroll+lines, 0), m.maxTranscriptScroll())
}

func (m *model) maxTranscriptScroll() int {
	return max(len(renderMarkdown(m.transcript.String(), max(m.width, 20)))-m.contentHeight(), 0)
}

func isASCIILetterOrSlash(value byte) bool {
	return value == '/' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
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
	next := []rune(strings.Join(kept, ", "))
	if string(next) == string(m.input) {
		return
	}
	m.saveInputUndo()
	m.input = next
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
	m.inputUndo = nil
}

func (m *model) setInput(value string) {
	m.input = []rune(value)
	m.cursor = len(m.input)
	m.inputUndo = nil
}

func (m *model) saveInputUndo() {
	snapshot := inputSnapshot{text: append([]rune(nil), m.input...), cursor: m.cursor}
	if len(m.inputUndo) == maxInputUndoEntries {
		copy(m.inputUndo, m.inputUndo[1:])
		m.inputUndo[len(m.inputUndo)-1] = snapshot
		return
	}
	m.inputUndo = append(m.inputUndo, snapshot)
}

func (m *model) undoInput() {
	if len(m.inputUndo) == 0 {
		return
	}
	last := m.inputUndo[len(m.inputUndo)-1]
	m.inputUndo = m.inputUndo[:len(m.inputUndo)-1]
	m.input, m.cursor = last.text, last.cursor
}

func (m *model) insertInput(value string) {
	insert := []rune(value)
	if len(insert) == 0 {
		return
	}
	m.cursor = min(max(m.cursor, 0), len(m.input))
	m.saveInputUndo()
	oldLength := len(m.input)
	m.input = append(m.input, insert...)
	copy(m.input[m.cursor+len(insert):], m.input[m.cursor:oldLength])
	copy(m.input[m.cursor:], insert)
	m.cursor += len(insert)
}

func (m *model) inputLineBounds() (int, int) {
	cursor := min(max(m.cursor, 0), len(m.input))
	start := cursor
	for start > 0 && m.input[start-1] != '\n' {
		start--
	}
	end := cursor
	for end < len(m.input) && m.input[end] != '\n' {
		end++
	}
	return start, end
}

func (m *model) moveInputCursorLine(direction int) {
	start, end := m.inputLineBounds()
	column := m.cursor - start
	if direction < 0 && start > 0 {
		m.cursor = start - 1
		targetStart, targetEnd := m.inputLineBounds()
		m.cursor = min(targetStart+column, targetEnd)
	} else if direction > 0 && end < len(m.input) {
		m.cursor = end + 1
		targetStart, targetEnd := m.inputLineBounds()
		m.cursor = min(targetStart+column, targetEnd)
	}
}

func (m *model) editInput(message tea.KeyPressMsg) {
	key, stroke := message.Key(), message.Keystroke()
	m.cursor = min(max(m.cursor, 0), len(m.input))
	switch {
	case stroke == "ctrl+z" || stroke == "super+z":
		m.undoInput()
	case key.Code == tea.KeyLeft:
		m.cursor = max(0, m.cursor-1)
	case key.Code == tea.KeyRight:
		m.cursor = min(len(m.input), m.cursor+1)
	case key.Code == tea.KeyHome:
		m.cursor, _ = m.inputLineBounds()
	case stroke == "ctrl+a":
		m.cursor = 0
	case key.Code == tea.KeyEnd:
		_, m.cursor = m.inputLineBounds()
	case stroke == "ctrl+e":
		m.cursor = len(m.input)
	case key.Code == tea.KeyBackspace && m.cursor > 0:
		m.saveInputUndo()
		copy(m.input[m.cursor-1:], m.input[m.cursor:])
		m.input = m.input[:len(m.input)-1]
		m.cursor--
	case key.Code == tea.KeyDelete && m.cursor < len(m.input):
		m.saveInputUndo()
		copy(m.input[m.cursor:], m.input[m.cursor+1:])
		m.input = m.input[:len(m.input)-1]
	case stroke == "ctrl+u" && len(m.input) > 0:
		m.saveInputUndo()
		m.input = nil
		m.cursor = 0
	case key.Text != "" && utf8.ValidString(key.Text):
		m.insertInput(key.Text)
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

func runShell(ctx context.Context, runner *agent.Runner, command string) tea.Cmd {
	return func() tea.Msg {
		output, err := runner.RunShell(ctx, command)
		return shellDoneEvent{command: command, output: output, err: err}
	}
}

func runCopy(runner *agent.Runner, n int) tea.Cmd {
	return func() tea.Msg {
		if runner == nil || strings.TrimSpace(runner.SessionPath) == "" {
			return copyDoneEvent{err: errors.New("session transcript is unavailable")}
		}
		messages, err := session.Transcript(runner.SessionPath)
		if err != nil {
			return copyDoneEvent{err: err}
		}
		assistant := make([]string, 0, len(messages))
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Role == "assistant" && strings.TrimSpace(messages[index].Text) != "" {
				assistant = append(assistant, messages[index].Text)
			}
		}
		if len(assistant) == 0 {
			return copyDoneEvent{}
		}
		if n > len(assistant) {
			return copyDoneEvent{err: fmt.Errorf("only %d assistant message(s) available", len(assistant))}
		}
		return copyDoneEvent{text: assistant[n-1]}
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
	if m.bridge != nil && m.bridge.PermissionMode() == tools.PermissionAuto {
		mode += "  \x1b[30;41m AUTO \x1b[0m"
	}
	if m.scrollFocused {
		mode += "  \x1b[30;46m SCROLLBACK \x1b[0m"
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
	contentLines := renderMarkdownWithLinks(content, width, m.hyperlinks)
	if m.historySearch != nil {
		contentLines = m.historySearchLines(width, m.contentHeight())
	} else if m.scrollSearch != nil {
		contentLines = m.scrollSearch.highlighted(contentLines)
	}
	visible := sliceFromBottom(contentLines, m.contentHeight(), m.scroll)
	for len(visible) < m.contentHeight() {
		visible = append(visible, "")
	}
	plainVisible := make([]string, len(visible))
	for index, line := range visible {
		plainVisible[index] = stripUIANSI(line)
	}
	if m.selection != nil {
		visible = m.selection.highlightedLines(visible)
	}
	body := strings.Join(visible, "\n")

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
	} else if m.scrollSearch != nil {
		footer = "\x1b[1;32mSearch scrollback\x1b[0m\n> " + renderInput(m.scrollSearch.query, m.scrollSearch.cursor, max(width-2, 1)) + "\n\x1b[2m" + truncate(m.scrollSearch.status(), width) + "\x1b[0m"
	} else {
		inputLines := []string{"> "}
		if m.running {
			inputLines = []string{"> "}
		} else {
			inputLines = renderPromptInput(m.input, m.cursor, width, m.visiblePromptInputRows())
		}
		hint := "Enter send · Shift/Alt-Enter newline · Ctrl-M multiline · Ctrl-Z undo"
		if m.multiline {
			hint = "Shift/Alt-Enter send · Enter newline · Ctrl-M single-line · Ctrl-Z undo"
		}
		footer = strings.Join(inputLines, "\n") + "\n\x1b[2m" + truncate(hint, width) + "\x1b[0m"
	}
	status := "\x1b[2m" + truncate(m.status, width) + "\x1b[0m"
	view := tea.NewView(header + "\n" + body + status + "\n" + footer)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	if !m.mouseReleased {
		view.MouseMode = tea.MouseModeCellMotion
	}
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
			if mouse.Button != tea.MouseLeft {
				return nil
			}
			if mouse.Y >= 1 && mouse.Y <= contentHeight {
				event := mouseSelectionEvent{phase: selectionStart, point: selectionPointForMouse(mouse, plainVisible), lines: plainVisible, at: time.Now()}
				return func() tea.Msg { return event }
			}
			if mouse.Y < contentHeight+3 {
				return nil
			}
			if event, ok := m.footerClick(mouse.X, mouse.Y, width); ok {
				return func() tea.Msg { return event }
			}
		case tea.MouseMotionMsg:
			if mouse.Button == tea.MouseLeft && m.selection != nil {
				event := mouseSelectionEvent{phase: selectionMove, point: selectionPointForMouse(mouse, m.selection.lines)}
				return func() tea.Msg { return event }
			}
		case tea.MouseReleaseMsg:
			if (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) && m.selection != nil {
				event := mouseSelectionEvent{phase: selectionRelease, point: selectionPointForMouse(mouse, m.selection.lines)}
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
	if m.scrollSearch != nil {
		return max(m.height-6, 3)
	}
	rows := 1
	if !m.running {
		rows = min(strings.Count(string(m.input), "\n")+1, m.visiblePromptInputRows())
	}
	return max(m.height-4-rows, 3)
}

func (m *model) visiblePromptInputRows() int {
	return min(maxPromptInputRows, max(m.height-6, 1))
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

func selectionPointForMouse(mouse tea.Mouse, lines []string) selectionPoint {
	if len(lines) == 0 {
		return selectionPoint{}
	}
	line := min(max(mouse.Y-1, 0), len(lines)-1)
	column := min(max(mouse.X, 0), max(displayWidth(lines[line])-1, 0))
	return selectionPoint{line: line, column: column}
}

func (m *model) countTextClick(at time.Time, point selectionPoint) uint8 {
	previous := m.selectionClick
	if previous.count > 0 && previous.line == point.line && !at.Before(previous.at) && at.Sub(previous.at) < textMultiClickWindow {
		return min(previous.count+1, uint8(3))
	}
	return 1
}

func (m *model) copyTextSelection() tea.Cmd {
	text := m.selection.text()
	if text == "" {
		m.selection = nil
		return nil
	}
	m.status = "selection copied"
	if m.selectionMode.holds() {
		return tea.SetClipboard(text)
	}
	nonce := m.selection.nonce
	return tea.Batch(
		tea.SetClipboard(text),
		tea.Tick(selectionHighlightTime, func(time.Time) tea.Msg { return selectionClearEvent{nonce: nonce} }),
	)
}

func (s *textSelection) bounds() (selectionPoint, selectionPoint) {
	start, end := s.anchor, s.head
	if start.line > end.line || start.line == end.line && start.column > end.column {
		start, end = end, start
	}
	return start, end
}

func (s *textSelection) text() string {
	if s.table != nil {
		if s.wholeTable || s.wholeCell || s.fromCell != s.toCell {
			return s.table.tsv(s.lines, s.fromCell, s.toCell)
		}
		return s.table.partialText(s.lines, s.fromCell, s.anchor, s.head)
	}
	if len(s.lines) == 0 {
		return ""
	}
	start, end := s.bounds()
	selected := make([]string, 0, end.line-start.line+1)
	for line := start.line; line <= end.line; line++ {
		from, to := 0, max(displayWidth(s.lines[line])-1, 0)
		if line == start.line {
			from = start.column
		}
		if line == end.line {
			to = end.column
		}
		selected = append(selected, selectDisplayColumns(s.lines[line], from, to))
	}
	return strings.Join(selected, "\n")
}

func (s *textSelection) highlightedLines(lines []string) []string {
	if s.table != nil {
		rangesByLine := s.table.partialRanges(s.fromCell, s.anchor, s.head)
		if s.wholeTable || s.wholeCell || s.fromCell != s.toCell {
			rangesByLine = s.table.ranges(s.fromCell, s.toCell)
		}
		for line, ranges := range rangesByLine {
			for index := len(ranges) - 1; index >= 0; index-- {
				left, right := displayColumnRuneRange(s.lines[line], ranges[index][0], ranges[index][1])
				runes := []rune(lines[line])
				if left < right {
					lines[line] = string(runes[:left]) + "\x1b[7m" + string(runes[left:right]) + "\x1b[0m" + string(runes[right:])
				}
			}
		}
		return lines
	}
	start, end := s.bounds()
	for line := start.line; line <= end.line && line < len(lines) && line < len(s.lines); line++ {
		from, to := 0, max(displayWidth(s.lines[line])-1, 0)
		if line == start.line {
			from = start.column
		}
		if line == end.line {
			to = end.column
		}
		plain := s.lines[line]
		left, right := displayColumnRuneRange(plain, from, to)
		runes := []rune(plain)
		if left < right {
			lines[line] = string(runes[:left]) + "\x1b[7m" + string(runes[left:right]) + "\x1b[0m" + string(runes[right:])
		}
	}
	return lines
}

func selectDisplayColumns(value string, from, to int) string {
	start, end := displayColumnRuneRange(value, from, to)
	return string([]rune(value)[start:end])
}

func displayColumnRuneRange(value string, from, to int) (int, int) {
	runes := []rune(value)
	start, end, column := len(runes), len(runes), 0
	for index, r := range runes {
		width := runeWidth(r)
		if width == 0 {
			continue
		}
		if start == len(runes) && from < column+width {
			start = index
		}
		if to < column+width {
			end = index + 1
			for end < len(runes) && runeWidth(runes[end]) == 0 {
				end++
			}
			break
		}
		column += width
	}
	if start == len(runes) {
		return len(runes), len(runes)
	}
	return start, end
}

func displayWidth(value string) int {
	width := 0
	for _, r := range value {
		width += runeWidth(r)
	}
	return width
}

func semanticDisplayRange(value string, column int, separators string) (int, int) {
	if start, end, ok := urlDisplayRange(value, column); ok {
		return start, end
	}
	return wordDisplayRange(value, column, separators)
}

func wordDisplayRange(value string, column int, separators string) (int, int) {
	type segment struct{ start, end, class int }
	segments, current := make([]segment, 0, len([]rune(value))), 0
	for _, r := range value {
		width := runeWidth(r)
		if width == 0 {
			continue
		}
		class := 0
		if unicode.IsSpace(r) {
			class = 1
		} else if strings.ContainsRune(separators, r) {
			class = 2
		}
		segments = append(segments, segment{start: current, end: current + width, class: class})
		current += width
	}
	if len(segments) == 0 {
		return 0, 0
	}
	target := len(segments) - 1
	for index, segment := range segments {
		if column >= segment.start && column < segment.end {
			target = index
			break
		}
	}
	left, right := target, target
	for left > 0 && segments[left-1].class == segments[target].class {
		left--
	}
	for right+1 < len(segments) && segments[right+1].class == segments[target].class {
		right++
	}
	return segments[left].start, segments[right].end
}

func urlDisplayRange(value string, column int) (int, int, bool) {
	for _, match := range selectionURLPattern.FindAllStringIndex(value, -1) {
		url := stripTrailingURLPunctuation(value[match[0]:match[1]])
		scheme := strings.Index(url, "://")
		if scheme >= 0 && scheme+3 == len(url) {
			continue
		}
		start := displayWidth(value[:match[0]])
		end := start + displayWidth(url)
		if column >= start && column < end {
			return start, end, true
		}
	}
	return 0, 0, false
}

func stripTrailingURLPunctuation(value string) string {
	for value != "" {
		r, size := utf8.DecodeLastRuneInString(value)
		if !strings.ContainsRune(".,:;!?)]}>\"'", r) {
			break
		}
		var opening rune
		switch r {
		case ')':
			opening = '('
		case ']':
			opening = '['
		case '}':
			opening = '{'
		case '>':
			opening = '<'
		}
		if opening != 0 && strings.Count(value, string(opening)) >= strings.Count(value, string(r)) {
			break
		}
		value = value[:len(value)-size]
	}
	return value
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

func renderPromptInput(input []rune, cursor, width, maxRows int) []string {
	logical := strings.Split(string(input), "\n")
	cursor = min(max(cursor, 0), len(input))
	active, column, remaining := 0, 0, cursor
	for index, line := range logical {
		length := len([]rune(line))
		if remaining <= length || index == len(logical)-1 {
			active, column = index, min(remaining, length)
			break
		}
		remaining -= length + 1
	}
	start := max(0, active-max(maxRows, 1)+1)
	end := min(len(logical), start+max(maxRows, 1))
	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		prefix := "  "
		if index == 0 {
			prefix = "> "
		}
		line := []rune(logical[index])
		if index == active {
			lines = append(lines, prefix+renderInput(line, column, max(width-2, 1)))
		} else {
			lines = append(lines, prefix+fitInputLine(line, max(width-2, 1)))
		}
	}
	return lines
}

func fitInputLine(input []rune, width int) string {
	used, end := 0, 0
	for end < len(input) {
		charWidth := runeWidth(input[end])
		if used+charWidth > width {
			break
		}
		used += charWidth
		end++
	}
	return string(input[:end])
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
	return ansi.Strip(value)
}
