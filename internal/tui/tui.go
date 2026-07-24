package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
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
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/billing"
	"github.com/lookcorner/go-cli/internal/changelog"
	"github.com/lookcorner/go-cli/internal/claudeimport"
	guides "github.com/lookcorner/go-cli/internal/docs"
	"github.com/lookcorner/go-cli/internal/imagine"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/terminaldiag"
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
	recapLabel                = "Recap \u2014 "
)

var selectionURLPattern = regexp.MustCompile(`(?i)\b(?:https?|ftp|file)://[^\s\x00-\x1f]+`)

var ErrNewSession = errors.New("start a new session")

type NewSessionError struct{ Prompt string }

func (e *NewSessionError) Error() string { return ErrNewSession.Error() }
func (e *NewSessionError) Unwrap() error { return ErrNewSession }

type ResumeSessionError struct {
	Path      string
	Workspace string
}

func (e *ResumeSessionError) Error() string { return "resume session " + e.Path }

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
type promptSuggestionEvent struct {
	text   string
	serial uint64
}
type rewindPointsEvent struct {
	points []session.RewindPoint
	err    error
	serial uint64
}
type rewindPreviewEvent struct {
	preview agent.RewindPreview
	err     error
	serial  uint64
}
type rewindDoneEvent struct {
	result agent.RewindResult
	err    error
	serial uint64
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
type exportDoneEvent struct {
	text string
	path string
	err  error
}
type feedbackDoneEvent struct{ err error }
type usageDoneEvent struct {
	text string
	err  error
}
type releaseNotesDoneEvent struct {
	text string
	err  error
}
type shareDoneEvent struct {
	url string
	err error
}
type authDoneEvent struct {
	action string
	err    error
}
type workspaceDoneEvent struct {
	path string
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
type recapDoneEvent struct {
	text   string
	err    error
	serial uint64
}
type btwDoneEvent struct {
	question string
	answer   string
	err      error
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
type claudeImportDoneEvent struct {
	result claudeimport.Result
	env    map[string]string
	err    error
}

type Bridge struct {
	ctx                   context.Context
	cancel                context.CancelFunc
	modeMu                sync.RWMutex
	mode                  tools.PermissionMode
	alwaysApproveLocked   bool
	autoModeLocked        bool
	asker                 tools.Approver
	persistPermissionMode func(string) error
	events                chan tea.Msg
	once                  sync.Once
	interactionMu         sync.Mutex
}

func NewBridge(parent context.Context, mode tools.PermissionMode) *Bridge {
	return NewBridgeWithLocks(parent, mode, false, false)
}

func NewBridgeWithAutoLock(parent context.Context, mode tools.PermissionMode, autoLocked bool) *Bridge {
	return NewBridgeWithLocks(parent, mode, autoLocked, false)
}

func NewBridgeWithLocks(parent context.Context, mode tools.PermissionMode, alwaysApproveLocked, autoModeLocked bool) *Bridge {
	if alwaysApproveLocked && mode == tools.PermissionAlwaysApprove || autoModeLocked && mode == tools.PermissionAuto {
		mode = tools.PermissionPrompt
	}
	ctx, cancel := context.WithCancel(parent)
	return &Bridge{
		ctx: ctx, cancel: cancel, mode: mode,
		alwaysApproveLocked: alwaysApproveLocked, autoModeLocked: autoModeLocked,
		events: make(chan tea.Msg, 1024),
	}
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
	case tools.PermissionAlwaysApprove:
		return nil
	case tools.PermissionDeny:
		return fmt.Errorf("permission denied for %s", action)
	case tools.PermissionPrompt, tools.PermissionAuto:
		if tools.PermissionBypassed(ctx) {
			return nil
		}
		if mode == tools.PermissionAuto && tools.AutoModeFastPath(action, detail) {
			return nil
		}
		if mode == tools.PermissionAuto {
			if allowed, available := tools.ClassifyPermission(ctx, action, detail); available {
				if allowed {
					return nil
				}
				return b.ask(ctx, action, detail)
			}
			if tools.AutoModeAllows(action, detail) {
				return nil
			}
		}
		return b.ask(ctx, action, detail)
	default:
		return fmt.Errorf("unknown permission mode %q", mode)
	}
}

func (b *Bridge) SetPromptApprover(asker tools.Approver) {
	b.modeMu.Lock()
	b.asker = asker
	b.modeMu.Unlock()
}

func (b *Bridge) SetPermissionModePersister(persist func(string) error) {
	b.modeMu.Lock()
	b.persistPermissionMode = persist
	b.modeMu.Unlock()
}

func (b *Bridge) ask(ctx context.Context, action, detail string) error {
	b.modeMu.RLock()
	asker := b.asker
	b.modeMu.RUnlock()
	if asker != nil {
		return asker.Approve(ctx, action, detail)
	}
	return b.prompt(ctx, action, detail)
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
	if enabled && b.alwaysApproveLocked {
		return errors.New("always-approve is disabled by managed policy")
	}
	return b.setPermissionModeLocked(enabled, tools.PermissionAlwaysApprove)
}

func (b *Bridge) SetAutoMode(enabled bool) error {
	b.modeMu.Lock()
	defer b.modeMu.Unlock()
	if enabled && b.mode == tools.PermissionDeny {
		return errors.New("auto permission mode is disabled by deny mode")
	}
	if enabled && b.autoModeLocked {
		return errors.New("auto permission mode is disabled")
	}
	return b.setPermissionModeLocked(enabled, tools.PermissionAuto)
}

func (b *Bridge) setPermissionModeLocked(enabled bool, enabledMode tools.PermissionMode) error {
	previous := b.mode
	b.mode = tools.PermissionPrompt
	if enabled {
		b.mode = enabledMode
	}
	if b.persistPermissionMode == nil {
		return nil
	}
	mode := string(b.mode)
	if b.mode == tools.PermissionPrompt {
		mode = "ask"
	}
	if err := b.persistPermissionMode(mode); err != nil {
		b.mode = previous
		return fmt.Errorf("persist permission mode: %w", err)
	}
	return nil
}

func (b *Bridge) AutoModeAvailable() bool {
	b.modeMu.RLock()
	defer b.modeMu.RUnlock()
	return !b.autoModeLocked && b.mode != tools.PermissionDeny
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
	ctx                context.Context
	runner             *agent.Runner
	bridge             *Bridge
	workspace          string
	modelName          string
	previousID         string
	inputTokens        int
	contextWindow      int
	transcript         strings.Builder
	input              []rune
	cursor             int
	inputUndo          []inputSnapshot
	multiline          bool
	history            []string
	historyIndex       int
	historyActive      bool
	historySearch      *historySearchState
	scrollSearch       *scrollSearchState
	selection          *textSelection
	selectionNonce     uint64
	selectionMode      textSelectionMode
	wordSeparators     string
	mouseToggle        bool
	vimMode            bool
	persistVimMode     func(bool) error
	compactMode        bool
	persistCompactMode func(bool) error
	showTimestamps     bool
	persistTimestamps  func(bool) error
	showTimeline       bool
	persistTimeline    func(bool) error
	themeName          string
	theme              themePalette
	persistTheme       func(string) error
	transcriptMessages []transcriptMessage
	scrollLines        int
	invertScroll       bool
	mouseReleased      bool
	hyperlinks         bool
	scrollFocused      bool
	selectionClick     selectionClickState
	width              int
	height             int
	scroll             int
	scrollTail         int
	scrollAnchor       *int
	running            bool
	status             string
	approval           *approvalEvent
	question           *questionState
	planMode           bool
	planReview         *planReviewState
	viewer             *readOnlyViewer
	remember           *rememberReviewState
	rememberInput      bool
	feedbackInput      bool
	rememberNonce      uint64
	turnCancel         context.CancelFunc
	initial            string
	pendingPrompts     []string
	scheduled          []tools.ScheduledTaskFired
	activeTask         string
	promptSerial       uint64
	newSession         bool
	newSessionPrompt   string
	resumeSession      *ResumeSessionError
	forkResult         *ForkSessionError
	forkSession        func(context.Context, bool) (ForkResult, error)
	forkInGit          bool

	promptSuggestion    string
	suggestionsEnabled  bool
	suggestionDismissed bool

	recapRunning  bool
	pendingRecap  string
	btwRunning    bool
	rewind        *rewindState
	jump          *jumpState
	timelineHover *timelineHit
	modelSelect   *modelSelectState
	settings      *settingsState
	docs          *docsState
	sessionSelect *sessionSelectState
	forkChoice    *forkChoiceState
	mcp           *mcpModal
	claudeImport  *claudeImportState
	extensions    *extensionsState
	agentConfig   *agentConfigState

	dashboard         *dashboardState
	dashboardDisabled bool
	dashboardPins     map[string]bool
	persistPins       func([]string) error
	dashboardOrder    []string
	persistOrder      func([]string) error
	dashboardGrouping string
	persistGrouping   func(string) error
	dashboardEpoch    uint64

	debug         debugState
	lastEmptyEsc  time.Time
	questionClick struct {
		option int
		at     time.Time
	}
}

type historySearchState struct {
	results  []string
	selected int
}

type claudeImportState struct {
	plan     claudeimport.Plan
	selected map[string]bool
	current  int
	busy     bool
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
	VimMode              bool
	SetVimMode           func(bool) error
	CompactMode          bool
	SetCompactMode       func(bool) error
	ShowTimestamps       bool
	SetShowTimestamps    func(bool) error
	ShowTimeline         bool
	SetShowTimeline      func(bool) error
	ScrollLines          *uint8
	InvertScroll         bool
	PromptSuggestions    bool
	Theme                string
	SetTheme             func(string) error
	ForkSession          func(context.Context, bool) (ForkResult, error)
	ForkInGit            bool
	DashboardPinned      []string
	DashboardDisabled    bool
	SetDashboardPinned   func([]string) error
	DashboardReorder     []string
	SetDashboardReorder  func([]string) error
	DashboardGrouping    string
	SetDashboardGrouping func(string) error
}

type transcriptMessage struct {
	start  int
	offset int
	at     time.Time
	role   string
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

type readOnlyViewer struct {
	title   string
	content string
	at      time.Time
}

type rememberReviewState struct {
	raw          string
	enhanced     string
	enhanceDone  bool
	showEnhanced bool
	nonce        uint64
}

type rewindPhase uint8

const (
	rewindLoading rewindPhase = iota
	rewindPicker
	rewindCancelOffer
	rewindCancelling
	rewindModeSelect
	rewindPreviewing
	rewindConfirm
	rewindExecuting
	rewindError
)

type rewindState struct {
	phase    rewindPhase
	points   []session.RewindPoint
	selected int
	target   int
	mode     agent.RewindMode
	preview  agent.RewindPreview
	err      string
}

type modelSelectPhase uint8

const (
	modelSelectModel modelSelectPhase = iota
	modelSelectEffort
	modelSelectError
)

type modelSelectState struct {
	phase      modelSelectPhase
	models     []agent.ModelOption
	efforts    []agent.ReasoningEffortOption
	selected   int
	model      agent.ModelOption
	effortOnly bool
	err        string
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
		contextWindow: runner.ContextWindow,
		status:        "ready", initial: strings.TrimSpace(initialPrompt), historyIndex: -1,
		history: loadPromptHistory(runner, workspace), selectionMode: parseTextSelectionMode(options.Mode),
		wordSeparators: defaultWordSeparators, mouseToggle: options.MouseReportingToggle, vimMode: options.VimMode, persistVimMode: options.SetVimMode,
		compactMode: options.CompactMode, persistCompactMode: options.SetCompactMode,
		showTimestamps: options.ShowTimestamps, persistTimestamps: options.SetShowTimestamps,
		showTimeline: options.ShowTimeline, persistTimeline: options.SetShowTimeline,
		scrollLines: mouseWheelScrollLines, invertScroll: options.InvertScroll,
		suggestionsEnabled: options.PromptSuggestions,
		hyperlinks:         detectTerminalHyperlinks(),
		themeName:          options.Theme,
		theme:              paletteFor(options.Theme),
		persistTheme:       options.SetTheme,
		forkSession:        options.ForkSession,
		forkInGit:          options.ForkInGit,
		dashboardDisabled:  options.DashboardDisabled,
		dashboardPins:      make(map[string]bool, len(options.DashboardPinned)),
		persistPins:        options.SetDashboardPinned,
		dashboardOrder:     append([]string(nil), options.DashboardReorder...),
		persistOrder:       options.SetDashboardReorder,
		dashboardGrouping:  dashboardGrouping(options.DashboardGrouping),
		persistGrouping:    options.SetDashboardGrouping,
		debug:              newDebugState(),
	}
	for _, id := range options.DashboardPinned {
		if id = strings.TrimSpace(id); id != "" {
			m.dashboardPins[id] = true
		}
	}
	defer m.debug.closeLog()
	if options.WordSeparators != nil {
		m.wordSeparators = *options.WordSeparators
	}
	if options.ScrollLines != nil {
		m.scrollLines = int(*options.ScrollLines)
	}
	if current, ok := runner.CurrentModel(); ok && strings.TrimSpace(current.Name) != "" {
		m.modelName = current.Name
	}
	if runner.Tools != nil {
		m.planMode = runner.Tools.PlanModeActive()
	}
	m.replaceTranscript(initialTranscript, nil)
	if runner != nil && strings.TrimSpace(runner.SessionPath) != "" {
		if messages, err := session.Transcript(runner.SessionPath); err == nil && strings.TrimSpace(session.FormatTranscript(messages)) == strings.TrimSpace(initialTranscript) {
			m.replaceTranscript(initialTranscript, messages)
		}
	}
	program := tea.NewProgram(m, tea.WithContext(ctx))
	final, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil
	}
	if err != nil {
		return err
	}
	if current, ok := final.(*model); ok && current.newSession {
		if current.newSessionPrompt != "" {
			return &NewSessionError{Prompt: current.newSessionPrompt}
		}
		return ErrNewSession
	}
	if current, ok := final.(*model); ok && current.resumeSession != nil {
		return current.resumeSession
	}
	if current, ok := final.(*model); ok && current.forkResult != nil {
		return current.forkResult
	}
	return nil
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
		if m.timelineWidth() == 0 {
			m.timelineHover = nil
		}
		if m.scrollAnchor != nil {
			m.anchorTranscriptMessage(*m.scrollAnchor)
		}
		m.refreshScrollSearch()
	case textEvent:
		m.selection = nil
		m.selectionClick = selectionClickState{}
		before := 0
		if m.scroll > 0 {
			before = len(renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors()))
		}
		m.transcript.WriteString(msg.text)
		if m.scroll > 0 {
			after := len(renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors()))
			m.scroll += max(after-before, 0)
		}
		m.refreshScrollSearch()
		return m, waitForBridge(m.bridge)
	case mouseScrollEvent:
		m.selection = nil
		m.selectionClick = selectionClickState{}
		m.timelineHover = nil
		m.clearTranscriptAnchor()
		before := m.scroll
		m.scroll = max(0, m.scroll+msg.lines)
		m.debug.recordScroll("wheel", msg.lines, before, m.scroll, m.maxTranscriptScroll(), m.contentHeight())
		return m, nil
	case timelineHoverEvent:
		m.timelineHover = msg.hit
		return m, nil
	case timelineJumpEvent:
		m.jumpTimeline(msg.turn)
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
		if m.rewind != nil && m.rewind.phase == rewindCancelling {
			return m, m.loadRewindPoints()
		}
		if m.pendingRecap != "" {
			m.appendSystem(recapLabel + m.pendingRecap)
			m.pendingRecap = ""
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
		if msg.err == nil && m.suggestionsEnabled {
			return m, runPromptSuggestion(m.ctx, m.runner, m.workspace, m.promptSerial)
		}
	case promptSuggestionEvent:
		if msg.serial == m.promptSerial && !m.running && strings.TrimSpace(msg.text) != "" {
			m.promptSuggestion = msg.text
			m.suggestionDismissed = false
		}
	case rewindPointsEvent:
		if m.rewind == nil || msg.serial != m.promptSerial || m.rewind.phase != rewindLoading {
			return m, nil
		}
		if msg.err != nil || len(msg.points) == 0 {
			m.rewind.phase = rewindError
			if msg.err != nil {
				m.rewind.err = msg.err.Error()
			} else {
				m.rewind.err = "No turns are available to rewind."
			}
			return m, nil
		}
		slices.Reverse(msg.points)
		m.rewind.points, m.rewind.phase, m.rewind.selected = msg.points, rewindPicker, 0
		m.status = "select a turn to rewind"
	case rewindPreviewEvent:
		if m.rewind == nil || msg.serial != m.promptSerial || m.rewind.phase != rewindPreviewing {
			return m, nil
		}
		if msg.err != nil {
			m.rewind.phase, m.rewind.err = rewindError, msg.err.Error()
			return m, nil
		}
		m.rewind.preview, m.rewind.phase = msg.preview, rewindConfirm
		m.status = "confirm rewind"
	case rewindDoneEvent:
		if m.rewind == nil || msg.serial != m.promptSerial || m.rewind.phase != rewindExecuting {
			return m, nil
		}
		if msg.err != nil {
			m.rewind.phase, m.rewind.err = rewindError, msg.err.Error()
			return m, nil
		}
		mode := msg.result.Mode
		if mode == agent.RewindAll || mode == agent.RewindConversationOnly {
			m.previousID = msg.result.PreviousResponseID
			m.replaceTranscript(session.FormatTranscript(msg.result.Messages), msg.result.Messages)
			m.setInput(msg.result.PromptText)
			m.history = loadPromptHistory(m.runner, m.workspace)
			m.historyActive, m.historyIndex = false, -1
		}
		m.rewind = nil
		m.scroll = 0
		m.status = fmt.Sprintf("rewound %s to turn %d", strings.ReplaceAll(string(mode), "_", " "), msg.result.Target+1)
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
		if command := m.startNext(); command != nil {
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
			if command := m.startNext(); command != nil {
				return m, tea.Batch(clipboard, command)
			}
			return m, clipboard
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case exportDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "export failed: " + msg.err.Error()
		} else if msg.path != "" {
			m.status = "conversation exported to " + msg.path
		} else {
			m.status = "conversation copied to clipboard"
			clipboard := tea.SetClipboard(msg.text)
			if command := m.startNext(); command != nil {
				return m, tea.Batch(clipboard, command)
			}
			return m, clipboard
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case feedbackDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			message := "Feedback could not be saved locally: " + msg.err.Error()
			m.appendSystem(message)
			m.status = "feedback failed"
		} else {
			message := "Feedback saved locally; no feedback server is configured for this session."
			m.appendSystem(message)
			m.status = "feedback saved"
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case usageDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.appendSystem("Usage could not be loaded: " + msg.err.Error())
			m.status = "usage failed"
		} else {
			m.appendSystem(msg.text)
			m.status = "usage"
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case releaseNotesDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.appendSystem(msg.err.Error())
			m.status = "release notes unavailable"
		} else {
			m.viewer = &readOnlyViewer{title: "Release Notes", content: msg.text, at: time.Now()}
			m.status = "release notes"
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case shareDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.appendSystem("Couldn't share session: " + msg.err.Error())
			m.status = "share failed"
		} else {
			m.appendSystem("Session shared: " + msg.url)
			m.status = "session shared"
		}
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
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
		if command := m.startNext(); command != nil {
			return m, command
		}
	case memoryDreamDoneEvent:
		m.running, m.turnCancel = false, nil
		if msg.err != nil {
			m.status = "memory dream failed: " + msg.err.Error()
		} else {
			m.status = "memory dream: " + msg.result.Outcome
		}
		if command := m.startNext(); command != nil {
			return m, command
		}
	case recapDoneEvent:
		m.recapRunning = false
		if msg.serial != m.promptSerial || errors.Is(msg.err, agent.ErrRecapSuperseded) {
			return m, nil
		}
		if msg.err != nil {
			if !m.running {
				if errors.Is(msg.err, agent.ErrRecapUnavailable) || errors.Is(msg.err, agent.ErrRecapInProgress) {
					m.status = "recap unavailable"
				} else {
					m.status = "recap failed: " + msg.err.Error()
				}
			}
			return m, nil
		}
		if m.running {
			m.pendingRecap = msg.text
		} else {
			m.appendSystem(recapLabel + msg.text)
			m.status = "recap"
		}
	case btwDoneEvent:
		m.btwRunning = false
		content := "**Question:** " + msg.question + "\n\n"
		if msg.err != nil {
			content += "**Error:** " + msg.err.Error()
			m.status = "side question failed"
		} else {
			content += "**Answer:** " + msg.answer
			m.status = "side question"
		}
		m.viewer = &readOnlyViewer{title: "Side question", content: content, at: time.Now()}
		m.scroll = 0
	case mcpDoneEvent:
		if m.mcp != nil {
			m.mcp.busy = false
			if msg.err != nil {
				m.mcp.err = msg.err.Error()
				m.status = "MCP update failed"
			} else {
				m.mcp.phase, m.mcp.server, m.mcp.input, m.mcp.cursor, m.mcp.err = mcpServers, "", nil, 0, ""
				m.mcp.refresh(m.runner)
				m.status = msg.action
			}
		}
	case authDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = msg.action + " failed: " + msg.err.Error()
			m.appendSystem("Authentication " + msg.action + " failed: " + msg.err.Error())
		} else {
			m.status = msg.action + " complete"
			m.appendSystem("Authentication " + msg.action + " complete; restarting session.")
			m.newSession = true
			return m, tea.Quit
		}
	case workspaceDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.status = "workspace change failed: " + msg.err.Error()
			m.appendSystem("Workspace change failed: " + msg.err.Error())
		} else {
			m.status = "changing workspace"
			m.resumeSession = &ResumeSessionError{Workspace: msg.path}
			return m, tea.Quit
		}
	case claudeImportDoneEvent:
		if m.claudeImport != nil {
			m.claudeImport.busy = false
			if msg.err != nil {
				m.status = "Claude import failed: " + msg.err.Error()
				return m, nil
			}
			if m.runner != nil && m.runner.Tools != nil {
				m.runner.Tools.OverlayEnvironment(msg.env)
			}
			m.claudeImport = nil
			message := fmt.Sprintf("Imported %d Claude setting(s).", msg.result.Imported)
			if len(msg.result.ModifiedFiles) > 0 {
				message += "\n\nModified:\n- " + strings.Join(msg.result.ModifiedFiles, "\n- ")
			}
			m.appendSystem(message)
			m.status = "Claude settings imported"
		}
	case extensionsEvent:
		return m.handleExtensionsEvent(msg)
	case dashboardDoneEvent:
		if m.dashboard != nil {
			m.dashboard.busy = false
			if msg.err != nil {
				m.dashboard.err = msg.err.Error()
				m.status = "dashboard action failed"
			} else if msg.action == "peek" {
				if slices.ContainsFunc(m.dashboard.rows, func(row dashboardRow) bool {
					return row.kind == dashboardSubagent && row.id == msg.id
				}) {
					m.dashboard.peekID = msg.id
					m.dashboard.peekKind = dashboardSubagent
					m.dashboard.peekTitle = "Subagent: " + msg.id
					m.dashboard.peekContent = msg.text
					m.scroll = 0
					m.status = "dashboard details"
				} else {
					m.dashboard.err = "Subagent no longer exists"
					m.status = "dashboard action failed"
				}
			} else {
				fallback := "task stopped"
				statusText := msg.text
				if msg.action == "delete" {
					m.dashboard.sessions = removeSession(m.dashboard.sessions, msg.id)
					fallback = "session deleted"
				} else if msg.action == "rename" {
					if msg.id == m.runner.SessionID {
						m.dashboard.currentTitle = msg.text
					} else {
						for i := range m.dashboard.sessions {
							if m.dashboard.sessions[i].SessionID == msg.id {
								m.dashboard.sessions[i].Title = msg.text
								break
							}
						}
					}
					fallback = "session renamed"
					statusText = ""
				}
				m.refreshDashboard()
				m.status = dashboardFirst(statusText, fallback)
			}
		}
	case dashboardLoadedEvent:
		return m, m.finishDashboardLoad(msg)
	case dashboardTickEvent:
		return m, m.handleDashboardTick(msg)
	case dashboardPollEvent:
		m.finishDashboardPoll(msg)
	case sessionSelectLoadedEvent:
		m.finishSessionSelectLoad(msg)
	case sessionSelectSearchRequestEvent:
		return m, m.startSessionSelectSearch(msg)
	case sessionSelectSearchEvent:
		m.finishSessionSelectSearch(msg)
	case sessionSelectDeleteEvent:
		m.finishSessionSelectDelete(msg)
	case forkDoneEvent:
		m.running = false
		m.turnCancel = nil
		if msg.err != nil {
			m.appendSystem("Couldn't fork session: " + msg.err.Error())
			m.status = "fork failed"
			return m, nil
		}
		m.forkResult = &ForkSessionError{Path: msg.result.Path, Workspace: msg.result.Workspace, Directive: msg.directive}
		m.status = "opening fork"
		return m, tea.Quit
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
	if m.question != nil {
		return m.handleQuestionKey(msg)
	}
	if m.rewind != nil {
		return m.handleRewindKey(msg)
	}
	if m.dashboard != nil {
		return m.handleDashboardKey(msg)
	}
	if m.jump != nil {
		return m.handleJumpKey(msg)
	}
	if m.mcp != nil {
		return m.handleMCPKey(msg)
	}
	if m.claudeImport != nil {
		return m.handleClaudeImportKey(msg)
	}
	if m.extensions != nil {
		return m.handleExtensionsKey(msg)
	}
	if m.agentConfig != nil {
		return m.handleAgentConfigKey(msg)
	}
	if m.settings != nil {
		return m.handleSettingsKey(msg)
	}
	if m.docs != nil {
		return m.handleDocsKey(msg)
	}
	if m.sessionSelect != nil {
		return m.handleSessionSelectKey(msg)
	}
	if m.forkChoice != nil {
		return m.handleForkChoiceKey(msg)
	}
	if m.modelSelect != nil {
		return m.handleModelSelectKey(msg)
	}
	if key.Code != tea.KeyEsc {
		m.lastEmptyEsc = time.Time{}
	}
	if m.remember != nil {
		return m.handleRememberReviewKey(msg)
	}
	if m.viewer != nil {
		if stroke == "ctrl+q" {
			return m, tea.Quit
		}
		switch {
		case key.Code == tea.KeyEsc:
			m.viewer = nil
			m.scroll = 0
			if m.running {
				m.status = "thinking"
			} else {
				m.status = "ready"
			}
		case stroke == "up" || stroke == "ctrl+k" || stroke == "pgup":
			m.scroll = min(m.scroll+max(m.contentHeight()/2, 1), m.maxViewerScroll())
		case stroke == "down" || stroke == "ctrl+j" || stroke == "pgdown":
			m.scroll = max(m.scroll-max(m.contentHeight()/2, 1), 0)
		case key.Code == tea.KeyHome:
			m.scroll = m.maxViewerScroll()
		case key.Code == tea.KeyEnd:
			m.scroll = 0
		}
		return m, nil
	}
	if m.historySearch != nil {
		return m.handleHistorySearchKey(msg)
	}
	if m.scrollSearch != nil && m.handleScrollSearchKey(msg) {
		return m, nil
	}
	if stroke == "ctrl+o" {
		if target := m.pinnedAnnouncementURL(); target != "" {
			if m.runner.OpenURL == nil || !m.runner.OpenURL(target) {
				m.appendSystem("Open: " + target)
			}
			m.status = "announcement link opened"
			return m, nil
		}
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
	if m.feedbackInput && key.Code == tea.KeyEsc {
		m.feedbackInput = false
		m.clearInput()
		m.status = "feedback cancelled"
		return m, nil
	}
	if !m.running {
		if remainder := m.promptSuggestionGhost(); remainder != "" && ((key.Code == tea.KeyTab && key.Mod == 0) || (key.Code == tea.KeyRight && key.Mod == 0)) {
			m.insertInput(remainder)
			m.promptSuggestion = ""
			m.suggestionDismissed = false
			m.status = "suggestion accepted"
			return m, nil
		}
		if key.Code == tea.KeyEsc && len(m.input) == 0 && m.promptSuggestion != "" {
			m.suggestionDismissed = true
			return m, nil
		}
		if key.Code == tea.KeyEsc && len(m.input) == 0 {
			now := time.Now()
			if !m.lastEmptyEsc.IsZero() && now.Sub(m.lastEmptyEsc) <= 800*time.Millisecond {
				m.lastEmptyEsc = time.Time{}
				return m, m.openRewind(false)
			}
			m.lastEmptyEsc = now
			m.status = "press Esc again to rewind"
			return m, nil
		}
	}
	if key.Code == tea.KeyTab && key.Mod == 0 {
		m.scrollFocused = true
		m.status = "scrollback focused"
		return m, nil
	}
	if m.running {
		return m.handleRunningKey(msg)
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
		if m.runner == nil || m.runner.Tools == nil {
			m.status = "plan mode unavailable"
			return m, nil
		}
		next := !m.planMode
		if err := m.setPlanMode(next); err != nil {
			m.status = "plan mode failed: " + err.Error()
			return m, nil
		}
		if next {
			m.status = "plan mode"
		} else {
			m.status = "ready"
		}
		return m, nil
	}
	if !m.rememberInput && m.insertNewlineForEnter(key) {
		return m, nil
	}
	switch key.Code {
	case tea.KeyEnter:
		prompt := strings.TrimSpace(string(m.input))
		if prompt == "" {
			if m.feedbackInput {
				m.feedbackInput = false
				m.appendSystem("Please provide feedback text.")
				m.status = "feedback required"
			}
			return m, nil
		}
		m.clearInput()
		if m.feedbackInput {
			m.feedbackInput = false
			return m, m.submitFeedback(prompt)
		}
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
		if command, ok := billing.ParseCommand(prompt); ok {
			switch command.Action {
			case billing.ShowUsage:
				m.running = true
				turnCtx, cancel := context.WithCancel(m.ctx)
				m.turnCancel = cancel
				m.status = "fetching usage"
				return m, runUsage(turnCtx, m.runner)
			case billing.ManageUsage:
				if m.runner == nil || m.runner.OpenURL == nil || !m.runner.OpenURL(billing.ManageURL) {
					m.appendSystem("Open: " + billing.ManageURL)
					m.status = "usage management link"
				} else {
					m.status = "usage management opened"
				}
				return m, nil
			default:
				m.appendSystem(command.Message)
				m.status = "usage argument invalid"
				return m, nil
			}
		}
		if changelog.IsCommand(prompt) {
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.status = "fetching release notes"
			return m, runReleaseNotes(turnCtx, m.runner)
		}
		if prompt == "/history" {
			m.openHistorySearch()
			return m, nil
		}
		if prompt == "/find" || strings.HasPrefix(prompt, "/find ") {
			m.openScrollSearch(strings.TrimSpace(strings.TrimPrefix(prompt, "/find")))
			return m, nil
		}
		if command, ok := imagine.Parse(prompt); ok && m.runner != nil && m.runner.Tools != nil && m.runner.Tools.HasTool(command.RequiredTool) {
			if command.Instruction == "" {
				m.appendSystem(command.Usage)
				m.status = "imagine description required"
				return m, nil
			}
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.rememberPrompt(command.Display)
			m.beginTurn(command.Display)
			return m, runTurnParts(turnCtx, m.runner, command.Display, command.Instruction, m.previousID)
		}
		fields := strings.Fields(prompt)
		switch fields[0] {
		case "/quit", "/exit":
			return m, tea.Quit
		case "/new", "/clear", "/home", "/welcome":
			m.newSession = true
			m.status = "starting new session"
			return m, tea.Quit
		case "/login", "/logout":
			if m.runner == nil {
				m.status = "authentication unavailable"
				m.appendSystem("Authentication is unavailable")
				return m, nil
			}
			action := strings.TrimPrefix(fields[0], "/")
			var operation func(context.Context) error
			if action == "login" {
				operation = m.runner.Login
			} else {
				operation = m.runner.Logout
			}
			if operation == nil {
				m.status = "authentication unavailable"
				m.appendSystem("Authentication " + action + " is unavailable")
				return m, nil
			}
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.status = action + " in progress"
			return m, runAuth(turnCtx, action, operation)
		case "/cd":
			if m.runner == nil || m.runner.ChangeWorkspace == nil {
				m.status = "workspace change unavailable"
				m.appendSystem("Workspace change is unavailable")
				return m, nil
			}
			path := strings.TrimSpace(strings.TrimPrefix(prompt, "/cd"))
			if path == "" {
				m.status = "workspace path required"
				m.appendSystem("Usage: /cd <path>")
				return m, nil
			}
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.status = "changing workspace"
			return m, runWorkspaceChange(turnCtx, m.runner, path)
		case "/help":
			permissionCommands := "`/always-approve`"
			if m.bridge != nil && m.bridge.AutoModeAvailable() {
				permissionCommands += " `/auto`"
			}
			mouseCommand := ""
			if m.mouseToggle {
				mouseCommand = " `/toggle-mouse-reporting`"
			}
			feedbackCommand := ""
			if m.runner != nil && m.runner.SubmitFeedback != nil {
				feedbackCommand = " `/feedback [text]`"
			}
			shareCommand := ""
			if m.runner != nil && m.runner.SharingEnabled != nil && m.runner.SharingEnabled() {
				shareCommand = " `/share`"
			}
			announcementCommand := ""
			if m.runner != nil && m.runner.Announcements != nil && m.runner.Announcements.Available() {
				announcementCommand = " `/announcements hide|show`"
			}
			mcpCommand := ""
			if m.runner != nil && m.runner.MCPServerCatalog != nil {
				mcpCommand = " `/mcps`"
			}
			extensionCommands := ""
			if m.runner != nil && (m.runner.HookCatalog != nil || m.runner.PluginInventory != nil || m.runner.MarketplaceList != nil || m.runner.Skills != nil) {
				extensionCommands = " `/hooks` `/plugins` `/marketplace` `/skills`"
			}
			agentCommands := ""
			if m.runner != nil && (m.runner.AgentDefinitions != nil || m.runner.Personas != nil) {
				agentCommands = " `/config-agents` (`/agents`) `/personas`"
			}
			imagineCommands := ""
			if m.runner != nil && m.runner.Tools != nil {
				if m.runner.Tools.HasTool(imagine.ImageTool) {
					imagineCommands += " `/imagine <description>`"
				}
				if m.runner.Tools.HasTool(imagine.VideoTool) {
					imagineCommands += " `/imagine-video <description>`"
				}
			}
			m.appendSystem("# Commands\n\n`! <command>` " + permissionCommands + announcementCommand + agentCommands + " `/btw <question>` `/cd <path>` `/compact` `/compact-mode` `/context` `/copy [N]` `/dashboard` (`/sessions`, `/agents-dashboard`) `/docs [web|title]` `/dream` `/effort [level]` `/exit` `/export [filename]` `/home` `/login` `/logout`" + feedbackCommand + " `/find` `/flush` `/fork [--worktree|--no-worktree] [directive]` `/help` `/history`" + extensionCommands + imagineCommands + " `/jump` `/loop` `/memory`" + mcpCommand + " `/model [name] [effort]` (`/m`) `/multiline` `/new` (`/clear`) `/plan [description]` `/privacy [opt-out]` `/queue` `/recap` `/release-notes` (`/changelog`) `/remember` `/rename <title>` `/resume` `/rewind` `/session-info` (`/status`, `/info`) `/settings`" + shareCommand + " `/tasks` `/terminal-setup` `/theme [name]` (`/t`)" + mouseCommand + " `/timeline` `/timestamps` `/transcript` `/usage [show|manage]` (`/cost`) `/view-plan` `/vim-mode`")
			m.status = "commands"
			return m, nil
		case "/docs", "/howto", "/guides":
			target := strings.TrimSpace(strings.TrimPrefix(prompt, fields[0]))
			switch strings.ToLower(target) {
			case "", "how-to", "howto", "guides", "guide", "list", "tui":
				m.openDocs()
			case "web", "online", "browser", "site", "www":
				if m.runner == nil || m.runner.OpenURL == nil || !m.runner.OpenURL(guides.BuildURL) {
					m.appendSystem("Open: " + guides.BuildURL)
					m.status = "documentation link"
				} else {
					m.status = "documentation opened"
				}
			default:
				guide, ok := guides.Find(target)
				if !ok {
					m.appendSystem(fmt.Sprintf("Unknown docs target %q. Try /docs, /docs web, or a guide title such as /docs Getting Started.", target))
					m.status = "docs target invalid"
					return m, nil
				}
				m.openGuide(guide, true)
			}
			return m, nil
		case "/settings", "/config", "/preferences", "/prefs":
			m.openSettings()
			return m, nil
		case "/debug", "/scroll-debug":
			m.handleDebugCommand(fields[0], strings.TrimSpace(strings.TrimPrefix(prompt, fields[0])))
			return m, nil
		case "/import-claude":
			m.openClaudeImport()
			return m, nil
		case "/hooks", "/plugins", "/marketplace", "/skills":
			return m, m.openExtensions(strings.TrimPrefix(fields[0], "/"))
		case "/config-agents", "/agents":
			m.openAgentConfig(agentConfigAgents)
			return m, nil
		case "/personas":
			m.openAgentConfig(agentConfigPersonas)
			return m, nil
		case "/mcps":
			m.openMCPModal()
			return m, nil
		case "/privacy":
			result, _ := agent.ParsePrivacyCommand(prompt)
			m.appendSystem(result.Message)
			m.status = result.Status
			return m, nil
		case "/share":
			if m.runner == nil || m.runner.SharingEnabled == nil || !m.runner.SharingEnabled() {
				m.appendSystem("Sharing is disabled")
				m.status = "sharing disabled"
				return m, nil
			}
			if strings.TrimSpace(m.runner.SessionID) == "" || m.runner.ShareSession == nil {
				m.appendSystem("No active session to share")
				m.status = "share unavailable"
				return m, nil
			}
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.status = "sharing session"
			return m, runShare(turnCtx, m.runner)
		case "/announcements":
			if m.runner == nil || m.runner.Announcements == nil || !m.runner.Announcements.Available() {
				m.appendSystem("No session announcements")
				m.status = "no announcements"
				return m, nil
			}
			if len(fields) < 2 || fields[1] != "hide" && fields[1] != "show" {
				m.appendSystem("Usage: /announcements hide | show")
				m.status = "announcement argument invalid"
				return m, nil
			}
			var err error
			if fields[1] == "hide" {
				err = m.runner.Announcements.Hide()
			} else {
				err = m.runner.Announcements.Show()
			}
			if err != nil {
				m.appendSystem("Couldn't update announcements: " + err.Error())
				m.status = "announcement update failed"
				return m, nil
			}
			m.status = "announcements " + fields[1]
			return m, nil
		case "/terminal-setup", "/terminal-check", "/terminal-info":
			m.appendSystem(terminaldiag.Report())
			m.status = "terminal setup"
			return m, nil
		case "/queue":
			m.appendSystem(formatPromptQueue(m.pendingPrompts))
			m.status = "queue"
			return m, nil
		case "/tasks":
			m.showTasks()
			return m, nil
		case "/dashboard", "/sessions", "/agents-dashboard":
			return m, m.openDashboard()
		case "/recap":
			return m, m.startRecap()
		case "/resume":
			return m, m.openSessionSelect()
		case "/fork":
			if m.recapRunning || m.btwRunning {
				m.appendSystem("Cannot fork while a background model request is running.")
				m.status = "fork busy"
				return m, nil
			}
			arguments := strings.TrimSpace(strings.TrimPrefix(prompt, "/fork"))
			parsed, err := parseForkArgs(arguments)
			if err != nil {
				m.appendSystem("Couldn't fork session: " + err.Error())
				m.status = "fork argument invalid"
				return m, nil
			}
			if parsed.worktree != nil {
				if *parsed.worktree && !m.forkInGit {
					m.appendSystem("Cannot create worktree: not in a Git repository.")
					m.status = "fork unavailable"
					return m, nil
				}
				return m, m.startFork(*parsed.worktree, parsed.directive)
			}
			if !m.forkInGit {
				return m, m.startFork(false, parsed.directive)
			}
			m.forkChoice = &forkChoiceState{directive: parsed.directive}
			m.status = "choose fork workspace"
			return m, nil
		case "/rewind":
			return m, m.openRewind(false)
		case "/jump":
			m.openJump()
			return m, nil
		case "/timeline":
			previous := m.showTimeline
			entries := m.jumpEntries()
			selected := m.activeJumpEntry(entries)
			m.showTimeline = !previous
			m.timelineHover = nil
			if m.persistTimeline != nil {
				if err := m.persistTimeline(m.showTimeline); err != nil {
					m.showTimeline = previous
					m.status = "persist timeline: " + err.Error()
					return m, nil
				}
			}
			if len(entries) > 0 {
				m.anchorTranscriptMessage(entries[selected].message)
			}
			state := "off"
			if m.showTimeline {
				state = "on"
			}
			m.status = "Timeline sidebar: " + state
			return m, nil
		case "/model", "/m":
			arguments := strings.TrimSpace(strings.TrimPrefix(prompt, fields[0]))
			if arguments == "" {
				m.openModelSelect(false)
				return m, nil
			}
			m.applyModelCommand(arguments)
			return m, nil
		case "/effort":
			level := strings.TrimSpace(strings.TrimPrefix(prompt, "/effort"))
			if level == "" {
				m.openModelSelect(true)
				return m, nil
			}
			m.applyModel(m.runner.ModelID, level)
			return m, nil
		case "/theme", "/t":
			m.applyThemeCommand(strings.TrimSpace(strings.TrimPrefix(prompt, fields[0])))
			return m, nil
		case "/btw":
			return m, m.startBtw(strings.TrimSpace(strings.TrimPrefix(prompt, "/btw")))
		case "/export":
			filename := strings.TrimSpace(strings.TrimPrefix(prompt, "/export"))
			m.running = true
			m.status = "exporting conversation"
			return m, runExport(m.runner, filename, m.workspace)
		case "/feedback":
			if m.runner == nil || m.runner.SubmitFeedback == nil {
				m.appendSystem(agent.FeedbackDisabledMessage)
				m.status = "feedback is disabled"
				return m, nil
			}
			text := strings.TrimSpace(strings.TrimPrefix(prompt, "/feedback"))
			if text == "" {
				m.feedbackInput = true
				m.status = "feedback mode"
				return m, nil
			}
			return m, m.submitFeedback(text)
		case "/timestamps":
			previous := m.showTimestamps
			m.showTimestamps = !previous
			if m.persistTimestamps != nil {
				if err := m.persistTimestamps(m.showTimestamps); err != nil {
					m.showTimestamps = previous
					m.status = "persist timestamps: " + err.Error()
					return m, nil
				}
			}
			state := "off"
			if m.showTimestamps {
				state = "on"
			}
			message := "Timestamps: " + state
			m.appendSystem(message)
			m.status = message
			return m, nil
		case "/compact-mode":
			previous := m.compactMode
			m.compactMode = !previous
			if m.persistCompactMode != nil {
				if err := m.persistCompactMode(m.compactMode); err != nil {
					m.compactMode = previous
					m.status = "persist compact mode: " + err.Error()
					return m, nil
				}
			}
			state := "off"
			if m.compactMode {
				state = "on"
			} else if m.effectiveCompact() {
				state = "off (auto-compact active on small terminal)"
			}
			message := "Compact mode: " + state
			m.appendSystem(message)
			m.status = message
			return m, nil
		case "/rename", "/title":
			title := strings.TrimSpace(strings.TrimPrefix(prompt, fields[0]))
			if err := m.runner.RenameSession(title); err != nil {
				m.status = "rename failed: " + err.Error()
				return m, nil
			}
			m.status = fmt.Sprintf("session renamed to %q", title)
			return m, nil
		case "/transcript", "/log":
			if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
				m.status = "no active session to view"
				return m, nil
			}
			messages, err := session.Transcript(m.runner.SessionPath)
			if err != nil {
				m.status = "transcript failed: " + err.Error()
				return m, nil
			}
			m.viewer = &readOnlyViewer{title: "Conversation transcript", content: session.FormatTranscript(messages)}
			m.status = "transcript"
			m.scroll = 0
			return m, nil
		case "/view-plan", "/show-plan", "/plan-view":
			if m.runner == nil || m.runner.Tools == nil {
				m.status = "plan preview unavailable"
				return m, nil
			}
			content, err := m.runner.Tools.CurrentPlan()
			if err != nil {
				m.status = "plan preview failed: " + err.Error()
				return m, nil
			}
			if strings.TrimSpace(content) == "" {
				content = "No plan content."
			}
			m.viewer = &readOnlyViewer{title: "Current plan", content: content}
			m.status = "plan preview"
			m.scroll = 0
			return m, nil
		case "/plan":
			if err := m.setPlanMode(true); err != nil {
				m.status = "plan mode failed: " + err.Error()
				return m, nil
			}
			description := strings.TrimSpace(strings.TrimPrefix(prompt, "/plan"))
			if description == "" {
				m.status = "plan mode"
				return m, nil
			}
			m.running = true
			turnCtx, cancel := context.WithCancel(m.ctx)
			m.turnCancel = cancel
			m.rememberPrompt(description)
			m.beginTurn(description)
			return m, runTurn(turnCtx, m.runner, description, m.previousID)
		case "/always-approve":
			if m.bridge == nil {
				m.status = "always-approve unavailable"
				return m, nil
			}
			enabled := m.bridge.PermissionMode() != tools.PermissionAlwaysApprove
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
		case "/auto":
			if m.bridge == nil || !m.bridge.AutoModeAvailable() {
				m.status = "auto permission mode is disabled"
				return m, nil
			}
			enabled := m.bridge.PermissionMode() != tools.PermissionAuto
			if err := m.bridge.SetAutoMode(enabled); err != nil {
				m.status = err.Error()
				return m, nil
			}
			if enabled {
				m.status = "auto permission mode"
			} else {
				m.status = "normal mode"
			}
			return m, nil
		case "/vim-mode":
			previous := m.vimMode
			m.vimMode = !previous
			if m.persistVimMode != nil {
				if err := m.persistVimMode(m.vimMode); err != nil {
					m.vimMode = previous
					m.status = "persist vim mode: " + err.Error()
					return m, nil
				}
			}
			state := "off"
			if m.vimMode {
				state = "on"
			}
			message := "Vim mode: " + state
			m.appendSystem(message)
			m.status = message
			return m, nil
		case "/toggle-mouse-reporting":
			if !m.mouseToggle {
				m.appendSystem("Mouse reporting toggle is off. Set `[ui] mouse_reporting_toggle = true` in ~/.grok/config.toml to enable it.")
				m.status = "mouse reporting toggle is off"
				return m, nil
			}
			m.toggleMouseReporting()
			return m, nil
		case "/session-info", "/status", "/info":
			if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
				m.status = "no active session"
				return m, nil
			}
			text := fmt.Sprintf("# Session info\n\n- Session: `%s`\n- Workspace: `%s`\n- Model: `%s`\n- Turn: %d", m.runner.SessionID, m.workspace, m.modelName, m.runner.SessionTurnCount())
			if m.contextWindow > 0 {
				text += fmt.Sprintf("\n- Context: %d / %d tokens (%d%%)", m.inputTokens, m.contextWindow, m.inputTokens*100/m.contextWindow)
			}
			m.appendSystem(text)
			m.status = "session info"
			return m, nil
		case "/context":
			if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
				m.status = "no active session"
				return m, nil
			}
			if m.contextWindow <= 0 {
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

func (m *model) submitFeedback(text string) tea.Cmd {
	m.running = true
	m.status = "saving feedback"
	return runFeedback(m.runner, text)
}

func (m *model) setPlanMode(active bool) error {
	if m.runner == nil || m.runner.Tools == nil {
		return errors.New("plan mode unavailable")
	}
	if err := m.runner.Tools.SetPlanMode(active); err != nil {
		return err
	}
	m.planMode = active
	return nil
}

func (m *model) appendSystem(text string) {
	m.clearTranscriptAnchor()
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n")
	}
	m.transcript.WriteString(strings.TrimSpace(text) + "\n")
	m.scroll = 0
}

func (m *model) handleRunningKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	if key.Code == tea.KeyEnter {
		if m.insertNewlineForEnter(key) {
			return m, nil
		}
		prompt := strings.TrimSpace(string(m.input))
		if prompt == "" {
			return m, nil
		}
		fields := strings.Fields(prompt)
		switch fields[0] {
		case "/queue":
			m.clearInput()
			m.appendSystem(formatPromptQueue(m.pendingPrompts))
			m.status = "queue"
			return m, nil
		case "/tasks":
			m.clearInput()
			m.showTasks()
			return m, nil
		case "/dashboard", "/sessions", "/agents-dashboard":
			m.clearInput()
			return m, m.openDashboard()
		case "/recap":
			m.clearInput()
			return m, m.startRecap()
		case "/rewind":
			m.clearInput()
			return m, m.openRewind(true)
		case "/btw":
			m.clearInput()
			return m, m.startBtw(strings.TrimSpace(strings.TrimPrefix(prompt, "/btw")))
		case "/fork":
			m.status = "cannot fork while a request is running"
			return m, nil
		}
		if strings.HasPrefix(prompt, "/") || strings.HasPrefix(prompt, "!") {
			m.status = "only prompts can be queued while a turn is running"
			return m, nil
		}
		m.clearInput()
		m.rememberPrompt(prompt)
		m.promptSerial++
		m.clearPromptSuggestion()
		m.pendingPrompts = append(m.pendingPrompts, prompt)
		m.status = fmt.Sprintf("queued prompt #%d", len(m.pendingPrompts))
		return m, nil
	}
	m.editInput(msg)
	return m, nil
}

func (m *model) openModelSelect(effortOnly bool) {
	if m.runner == nil || m.runner.ResolveModel == nil {
		m.status = "model switching unavailable"
		return
	}
	if m.recapRunning || m.btwRunning {
		m.status = "wait for the background model request before switching models"
		return
	}
	state := &modelSelectState{models: m.runner.AvailableModels(), effortOnly: effortOnly}
	if effortOnly {
		current, ok := m.runner.CurrentModel()
		if !ok {
			m.status = "no active model"
			return
		}
		state.model = current
		state.efforts = m.runner.CurrentReasoningEfforts()
		if len(state.efforts) == 0 {
			m.status = fmt.Sprintf("model %q does not support reasoning effort", current.Name)
			return
		}
		state.phase = modelSelectEffort
		state.selected = selectedEffort(state.efforts, m.runner.ReasoningEffort)
	} else {
		if len(state.models) == 0 {
			m.status = "no selectable models"
			return
		}
		state.phase = modelSelectModel
		for index, option := range state.models {
			if option.ID == m.runner.ModelID {
				state.selected = index
				break
			}
		}
	}
	m.promptSerial++
	m.clearPromptSuggestion()
	m.modelSelect = state
	m.scroll = 0
	m.status = "select a model"
	if effortOnly {
		m.status = "select reasoning effort"
	}
}

func (m *model) handleModelSelectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.modelSelect
	key := msg.Key()
	if key.Code == tea.KeyEsc {
		if state.phase == modelSelectEffort && !state.effortOnly {
			state.phase = modelSelectModel
			state.efforts = nil
			state.selected = 0
			for index, option := range state.models {
				if option.ID == state.model.ID {
					state.selected = index
					break
				}
			}
			m.status = "select a model"
			return m, nil
		}
		m.modelSelect = nil
		m.status = "ready"
		return m, nil
	}
	if state.phase == modelSelectError {
		if key.Code == tea.KeyEnter {
			m.modelSelect = nil
			m.status = "ready"
		}
		return m, nil
	}
	items := len(state.models)
	if state.phase == modelSelectEffort {
		items = len(state.efforts)
	}
	if key.Code == tea.KeyUp || key.Text == "k" {
		state.selected = max(0, state.selected-1)
		return m, nil
	}
	if key.Code == tea.KeyDown || key.Text == "j" {
		state.selected = min(items-1, state.selected+1)
		return m, nil
	}
	if key.Code != tea.KeyEnter || items == 0 {
		return m, nil
	}
	if state.phase == modelSelectModel {
		state.model = state.models[state.selected]
		if state.model.SupportsReasoningEffort {
			state.efforts = agent.ReasoningEfforts(state.model)
			state.phase = modelSelectEffort
			current := state.model.ReasoningEffort
			if state.model.ID == m.runner.ModelID {
				current = m.runner.ReasoningEffort
			}
			state.selected = selectedEffort(state.efforts, current)
			m.status = "select reasoning effort"
			return m, nil
		}
		m.applyModelCommand(state.model.ID)
		return m, nil
	}
	effort := state.efforts[state.selected]
	m.applyModel(state.model.ID, effort.ID)
	return m, nil
}

func (m *model) applyModelCommand(arguments string) {
	if m.runner == nil {
		m.status = "model switching unavailable"
		return
	}
	if m.recapRunning || m.btwRunning {
		m.status = "wait for the background model request before switching models"
		return
	}
	option, err := m.runner.SwitchModelCommand(arguments)
	m.finishModelSwitch(option, err)
}

func (m *model) applyModel(id, effort string) {
	if m.runner == nil {
		m.status = "model switching unavailable"
		return
	}
	if m.recapRunning || m.btwRunning {
		m.status = "wait for the background model request before switching models"
		return
	}
	option, err := m.runner.SwitchModel(id, effort)
	m.finishModelSwitch(option, err)
}

func (m *model) finishModelSwitch(option agent.ModelOption, err error) {
	if err != nil && option.ID == "" {
		if m.modelSelect != nil {
			m.modelSelect.phase, m.modelSelect.err = modelSelectError, err.Error()
		}
		m.status = "model switch failed: " + err.Error()
		return
	}
	m.modelSelect = nil
	m.previousID = ""
	m.inputTokens = 0
	m.contextWindow = m.runner.ContextWindow
	m.modelName = option.Name
	if strings.TrimSpace(m.modelName) == "" {
		m.modelName = option.Model
	}
	m.promptSerial++
	m.clearPromptSuggestion()
	m.status = "model switched to " + m.modelName
	if m.runner.ReasoningEffort != "" {
		m.status += " (" + m.runner.ReasoningEffort + ")"
	}
	if err != nil {
		m.status += "; " + err.Error()
	}
}

func selectedEffort(efforts []agent.ReasoningEffortOption, current string) int {
	for index, effort := range efforts {
		if matchesEffort(effort, current) {
			return index
		}
	}
	for index, effort := range efforts {
		if effort.Default {
			return index
		}
	}
	return 0
}

func matchesEffort(effort agent.ReasoningEffortOption, current string) bool {
	current = strings.TrimSpace(current)
	if current == "" {
		return false
	}
	value := effort.Value
	if value == "" {
		value = effort.ID
	}
	return strings.EqualFold(effort.ID, current) || strings.EqualFold(value, current)
}

func (m *model) showTasks() {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		m.status = "no active session"
		return
	}
	m.appendSystem(formatTaskSnapshot(m.runner.TaskSnapshot(), time.Now()))
	m.status = "tasks"
}

func (m *model) openRewind(cancelTurn bool) tea.Cmd {
	if m.runner == nil {
		m.status = "rewind unavailable"
		return nil
	}
	if m.btwRunning {
		m.status = "wait for the side question before rewinding"
		return nil
	}
	m.promptSerial++
	m.clearPromptSuggestion()
	m.lastEmptyEsc = time.Time{}
	if cancelTurn && m.running {
		m.rewind = &rewindState{phase: rewindCancelOffer}
		m.status = "cancel the current turn to rewind?"
		return nil
	}
	if m.running {
		m.status = "cancel the current turn before rewinding"
		return nil
	}
	m.rewind = &rewindState{phase: rewindLoading}
	m.status = "loading rewind points"
	return runRewindPoints(m.runner, m.promptSerial)
}

func (m *model) loadRewindPoints() tea.Cmd {
	if m.rewind == nil {
		m.rewind = &rewindState{}
	}
	m.rewind.phase = rewindLoading
	m.status = "loading rewind points"
	return runRewindPoints(m.runner, m.promptSerial)
}

func (m *model) handleRewindKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	state := m.rewind
	dismiss := func() (tea.Model, tea.Cmd) {
		m.rewind = nil
		if m.running {
			m.status = "thinking"
		} else {
			m.status = "ready"
		}
		return m, nil
	}
	if key.Code == tea.KeyEsc {
		switch state.phase {
		case rewindModeSelect:
			state.phase, state.selected = rewindPicker, 0
			return m, nil
		case rewindConfirm:
			state.phase, state.selected = rewindModeSelect, 0
			return m, nil
		case rewindCancelling, rewindExecuting:
			return m, nil
		default:
			return dismiss()
		}
	}
	switch state.phase {
	case rewindCancelOffer:
		switch {
		case strings.EqualFold(key.Text, "y"):
			state.selected = 0
		case strings.EqualFold(key.Text, "n"):
			return dismiss()
		case key.Code == tea.KeyUp || key.Code == tea.KeyDown:
			state.selected = 1 - state.selected
			return m, nil
		case key.Code != tea.KeyEnter:
			return m, nil
		}
		if state.selected == 1 {
			return dismiss()
		}
		m.pendingPrompts = nil
		state.phase = rewindCancelling
		m.status = "cancelling turn before rewind"
		if m.turnCancel != nil {
			m.turnCancel()
			return m, nil
		}
		return m, m.loadRewindPoints()
	case rewindPicker:
		if key.Code == tea.KeyUp || key.Text == "k" {
			state.selected = max(0, state.selected-1)
			return m, nil
		}
		if key.Code == tea.KeyDown || key.Text == "j" {
			state.selected = min(len(state.points)-1, state.selected+1)
			return m, nil
		}
		if key.Code == tea.KeyEnter && len(state.points) > 0 {
			state.target = state.points[state.selected].PromptIndex
			state.phase, state.selected = rewindModeSelect, 0
		}
	case rewindModeSelect:
		modes := rewindModes(state.points, state.target)
		if key.Code == tea.KeyUp || key.Text == "k" {
			state.selected = max(0, state.selected-1)
			return m, nil
		}
		if key.Code == tea.KeyDown || key.Text == "j" {
			state.selected = min(len(modes)-1, state.selected+1)
			return m, nil
		}
		if key.Code == tea.KeyEnter && len(modes) > 0 {
			state.mode = modes[state.selected]
			state.phase = rewindPreviewing
			m.status = "previewing rewind"
			return m, runRewindPreview(m.runner, state.target, state.mode, m.promptSerial)
		}
	case rewindConfirm:
		if strings.EqualFold(key.Text, "n") {
			state.phase, state.selected = rewindModeSelect, 0
			return m, nil
		}
		if key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y") {
			state.phase = rewindExecuting
			m.status = "rewinding"
			return m, runRewind(m.runner, state.target, state.mode, m.promptSerial)
		}
	case rewindError:
		if key.Code == tea.KeyEnter {
			return dismiss()
		}
	}
	return m, nil
}

func rewindModes(points []session.RewindPoint, target int) []agent.RewindMode {
	for _, point := range points {
		if point.PromptIndex == target && point.HasFileChanges {
			return []agent.RewindMode{agent.RewindAll, agent.RewindConversationOnly, agent.RewindFilesOnly}
		}
	}
	return []agent.RewindMode{agent.RewindAll, agent.RewindConversationOnly}
}

func (m *model) startRecap() tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		m.status = "no active session"
		return nil
	}
	if m.recapRunning {
		if !m.running {
			m.status = "recap already in progress"
		}
		return nil
	}
	m.recapRunning = true
	if !m.running {
		m.status = "generating recap"
	}
	return runRecap(m.ctx, m.runner, m.previousID, m.promptSerial)
}

func (m *model) startBtw(question string) tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" || strings.TrimSpace(m.runner.SessionPath) == "" {
		m.status = "no active session"
		return nil
	}
	if strings.TrimSpace(question) == "" {
		m.status = "usage: /btw <question>"
		return nil
	}
	if m.btwRunning {
		if !m.running {
			m.status = "side question already in progress"
		}
		return nil
	}
	m.btwRunning = true
	if !m.running {
		m.status = "asking side question"
	}
	return runBtw(m.ctx, m.runner, question, m.previousID)
}

func (m *model) insertNewlineForEnter(key tea.Key) bool {
	if key.Code != tea.KeyEnter {
		return false
	}
	modified := key.Mod&(tea.ModShift|tea.ModAlt) != 0
	if !modified && len(m.input) > 0 && m.cursor == len(m.input) && m.input[len(m.input)-1] == '\\' {
		m.saveInputUndo()
		m.input[len(m.input)-1] = '\n'
		return true
	}
	if (!m.multiline && modified) || (m.multiline && !modified) {
		m.insertInput("\n")
		return true
	}
	return false
}

func formatPromptQueue(prompts []string) string {
	if len(prompts) == 0 {
		return "Queue is empty."
	}
	label := "prompt"
	if len(prompts) != 1 {
		label = "prompts"
	}
	var result strings.Builder
	fmt.Fprintf(&result, "Queued %s (%d):", label, len(prompts))
	for index, prompt := range prompts {
		first, _, _ := strings.Cut(prompt, "\n")
		fmt.Fprintf(&result, "\n  #%d  %s", index+1, strings.TrimSpace(first))
		if extra := strings.Count(prompt, "\n"); extra > 0 {
			suffix := ""
			if extra != 1 {
				suffix = "s"
			}
			fmt.Fprintf(&result, "  (+%d more line%s)", extra, suffix)
		}
	}
	return result.String()
}

func formatTaskSnapshot(snapshot agent.TaskSnapshot, now time.Time) string {
	subagents := slices.Clone(snapshot.Subagents)
	sort.Slice(subagents, func(i, j int) bool {
		iRunning, jRunning := subagents[i].Status == "running", subagents[j].Status == "running"
		if iRunning != jRunning {
			return iRunning
		}
		if subagents[i].StartedAtMS != subagents[j].StartedAtMS {
			return subagents[i].StartedAtMS > subagents[j].StartedAtMS
		}
		return subagents[i].ID < subagents[j].ID
	})
	processes := slices.Clone(snapshot.Processes)
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Completed != processes[j].Completed {
			return !processes[i].Completed
		}
		if processes[i].StartTime.SecsSinceEpoch != processes[j].StartTime.SecsSinceEpoch {
			return processes[i].StartTime.SecsSinceEpoch > processes[j].StartTime.SecsSinceEpoch
		}
		if processes[i].StartTime.NanosSinceEpoch != processes[j].StartTime.NanosSinceEpoch {
			return processes[i].StartTime.NanosSinceEpoch > processes[j].StartTime.NanosSinceEpoch
		}
		return processes[i].TaskID < processes[j].TaskID
	})
	scheduled := slices.Clone(snapshot.Scheduled)
	sort.Slice(scheduled, func(i, j int) bool {
		if scheduled[i].HumanSchedule != scheduled[j].HumanSchedule {
			return scheduled[i].HumanSchedule < scheduled[j].HumanSchedule
		}
		return scheduled[i].TaskID < scheduled[j].TaskID
	})

	rows := make([]string, 0, len(subagents)+len(processes)+len(scheduled))
	for _, task := range subagents {
		status := task.Status
		if status == "" {
			status = "done"
		}
		label := strings.TrimSpace(task.Type + " · " + task.Description)
		label = strings.Trim(label, " ·")
		rows = append(rows, fmt.Sprintf("  %-10s%s  (%s)", status, label, formatTaskDuration(time.Duration(task.DurationMS)*time.Millisecond)))
	}
	for _, task := range processes {
		status := "running"
		if task.Completed {
			status = "done"
			if task.ExplicitlyKilled {
				status = "killed"
			} else if task.Signal != nil || task.ExitCode != nil && *task.ExitCode != 0 {
				status = "failed"
			}
		}
		kind := "Task"
		if task.Kind == "monitor" {
			kind = "Monitor"
		}
		description := firstNonemptyLine(task.Description)
		if description == "" {
			description = firstNonemptyLine(task.Command)
		}
		rows = append(rows, fmt.Sprintf("  %-10s%s · %s  (%s)", status, kind, description, formatTaskDuration(processDuration(task, now))))
	}
	for _, task := range scheduled {
		rows = append(rows, fmt.Sprintf("  %-10s%s · %s", "scheduled", task.HumanSchedule, firstNonemptyLine(task.Prompt)))
	}
	if len(rows) == 0 {
		return "No background tasks or subagents."
	}
	label := "Task"
	if len(rows) != 1 {
		label = "Tasks"
	}
	return fmt.Sprintf("%s (%d):\n%s", label, len(rows), strings.Join(rows, "\n"))
}

func processDuration(task tools.ProcessSnapshot, now time.Time) time.Duration {
	start := time.Unix(task.StartTime.SecsSinceEpoch, int64(task.StartTime.NanosSinceEpoch))
	end := now
	if task.EndTime != nil {
		end = time.Unix(task.EndTime.SecsSinceEpoch, int64(task.EndTime.NanosSinceEpoch))
	}
	return max(0, end.Sub(start))
}

func formatTaskDuration(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	return duration.Round(time.Second).String()
}

func firstNonemptyLine(text string) string {
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
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

func (m *model) toggleMouseReporting() {
	m.mouseReleased = !m.mouseReleased
	m.selection = nil
	m.selectionClick = selectionClickState{}
	if m.mouseReleased {
		m.status = "mouse reporting disabled"
	} else {
		m.status = "mouse reporting enabled"
	}
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
		m.toggleMouseReporting()
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
	case m.vimMode && key.Mod == 0 && key.Text == "k":
		m.scrollTranscript(1)
	case m.vimMode && key.Mod == 0 && key.Text == "j":
		m.scrollTranscript(-1)
	case m.vimMode && key.Mod == 0 && key.Text == "g":
		m.scrollTranscript(m.maxTranscriptScroll())
	case m.vimMode && key.Mod == 0 && key.Text == "G":
		m.scrollTranscript(-m.scroll)
	case m.vimMode && key.Mod == 0 && key.Text == "/":
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
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
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
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	target := m.scrollSearch.matches[m.scrollSearch.current].line
	height := m.contentHeight()
	maxStart := max(len(lines)-height, 0)
	start := min(max(target-height/2, 0), maxStart)
	m.scroll = maxStart - start
}

func (m *model) scrollTranscript(lines int) {
	m.selection = nil
	m.selectionClick = selectionClickState{}
	m.clearTranscriptAnchor()
	before := m.scroll
	m.scroll = min(max(m.scroll+lines, 0), m.maxTranscriptScroll())
	m.debug.recordScroll("keyboard", lines, before, m.scroll, m.maxTranscriptScroll(), m.contentHeight())
}

func (m *model) maxTranscriptScroll() int {
	return max(len(renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors()))+m.scrollTail-m.contentHeight(), 0)
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

func (m *model) replaceTranscript(text string, messages []session.Message) {
	m.clearTranscriptAnchor()
	text = strings.TrimSpace(text)
	m.transcript.Reset()
	m.transcript.WriteString(text)
	m.transcriptMessages = nil
	if text == "" || strings.TrimSpace(session.FormatTranscript(messages)) != text {
		return
	}
	offset := 0
	for index, message := range messages {
		if index > 0 {
			offset += 2
		}
		label := "Gork"
		if message.Role == "user" {
			label = "You"
		}
		m.transcriptMessages = append(m.transcriptMessages, transcriptMessage{start: offset, offset: offset + len(label), at: message.Time, role: message.Role})
		offset += len(session.FormatTranscript([]session.Message{message}))
	}
}

func (m *model) transcriptText() string {
	text := m.transcript.String()
	if len(m.transcriptMessages) == 0 || !m.showTimestamps && !m.effectiveCompact() {
		return text
	}
	var rendered strings.Builder
	start := 0
	for index, message := range m.transcriptMessages {
		if message.start < start || message.offset < message.start || message.offset > len(text) {
			continue
		}
		previousUser := index > 0 && m.transcriptMessages[index-1].role == "user"
		if m.effectiveCompact() && (message.role == "user" || previousUser) && message.start >= start+2 && text[message.start-2:message.start] == "\n\n" {
			rendered.WriteString(text[start : message.start-1])
		} else {
			rendered.WriteString(text[start:message.start])
		}
		rendered.WriteString(text[message.start:message.offset])
		if m.showTimestamps && !message.at.IsZero() {
			rendered.WriteString("  " + message.at.Local().Format("3:04 PM"))
		}
		start = message.offset
	}
	rendered.WriteString(text[start:])
	return rendered.String()
}

func (m *model) effectiveCompact() bool {
	return m.compactMode || m.height > 0 && m.height <= 20
}

func (m *model) beginTurn(prompt string) {
	m.promptSerial++
	m.clearPromptSuggestion()
	m.clearTranscriptAnchor()
	if m.transcript.Len() > 0 {
		m.transcript.WriteString("\n")
	}
	now := time.Now()
	messageStart := m.transcript.Len()
	m.transcript.WriteString("You")
	m.transcriptMessages = append(m.transcriptMessages, transcriptMessage{start: messageStart, offset: m.transcript.Len(), at: now, role: "user"})
	m.transcript.WriteString("\n" + prompt + "\n\nGork")
	m.transcriptMessages = append(m.transcriptMessages, transcriptMessage{start: m.transcript.Len() - len("Gork"), offset: m.transcript.Len(), at: now, role: "assistant"})
	m.transcript.WriteString("\n")
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

func runTurnParts(ctx context.Context, runner *agent.Runner, display, instruction, previousID string) tea.Cmd {
	return func() tea.Msg {
		parts := []api.ContentPart{{Type: "input_text", Text: instruction}}
		result, err := runner.RunTurnParts(ctx, display, parts, previousID)
		return turnDoneEvent{result: result, err: err}
	}
}

func runFeedback(runner *agent.Runner, text string) tea.Cmd {
	return func() tea.Msg {
		if runner == nil || runner.SubmitFeedback == nil {
			return feedbackDoneEvent{err: errors.New("feedback is disabled")}
		}
		turn := int64(max(0, runner.SessionTurnCount()-1))
		err := runner.SubmitFeedback(session.UserFeedback{
			TurnNumber: &turn, ClientType: "tui", Text: strings.TrimSpace(text),
			ModelID: runner.ModelID, ResolvedModelID: runner.Model,
		})
		return feedbackDoneEvent{err: err}
	}
}

func runUsage(ctx context.Context, runner *agent.Runner) tea.Cmd {
	return func() tea.Msg {
		if runner == nil || runner.FetchUsage == nil {
			return usageDoneEvent{err: errors.New("billing usage is unavailable")}
		}
		text, err := runner.FetchUsage(ctx)
		return usageDoneEvent{text: text, err: err}
	}
}

func runReleaseNotes(ctx context.Context, runner *agent.Runner) tea.Cmd {
	return func() tea.Msg {
		if runner == nil || runner.FetchReleaseNotes == nil {
			return releaseNotesDoneEvent{err: changelog.ErrUnavailable}
		}
		text, err := runner.FetchReleaseNotes(ctx)
		return releaseNotesDoneEvent{text: text, err: err}
	}
}

func runShare(ctx context.Context, runner *agent.Runner) tea.Cmd {
	return func() tea.Msg {
		url, err := runner.ShareSession(ctx)
		return shareDoneEvent{url: url, err: err}
	}
}

func runAuth(ctx context.Context, action string, operation func(context.Context) error) tea.Cmd {
	return func() tea.Msg { return authDoneEvent{action: action, err: operation(ctx)} }
}

func runWorkspaceChange(ctx context.Context, runner *agent.Runner, path string) tea.Cmd {
	return func() tea.Msg {
		resolved, err := runner.ChangeWorkspace(ctx, path)
		return workspaceDoneEvent{path: resolved, err: err}
	}
}

func runRecap(ctx context.Context, runner *agent.Runner, previousID string, serial uint64) tea.Cmd {
	return func() tea.Msg {
		text, err := runner.Recap(ctx, previousID)
		return recapDoneEvent{text: text, err: err, serial: serial}
	}
}

func runPromptSuggestion(ctx context.Context, runner *agent.Runner, cwd string, serial uint64) tea.Cmd {
	return func() tea.Msg {
		text, _ := runner.SuggestPrompt(ctx, cwd, "")
		return promptSuggestionEvent{text: text, serial: serial}
	}
}

func runRewindPoints(runner *agent.Runner, serial uint64) tea.Cmd {
	return func() tea.Msg {
		points, err := runner.RewindPoints()
		return rewindPointsEvent{points: points, err: err, serial: serial}
	}
}

func runRewindPreview(runner *agent.Runner, target int, mode agent.RewindMode, serial uint64) tea.Cmd {
	return func() tea.Msg {
		preview, err := runner.PreviewRewind(target, mode)
		return rewindPreviewEvent{preview: preview, err: err, serial: serial}
	}
}

func runRewind(runner *agent.Runner, target int, mode agent.RewindMode, serial uint64) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.ExecuteRewind(target, mode)
		return rewindDoneEvent{result: result, err: err, serial: serial}
	}
}

func runBtw(ctx context.Context, runner *agent.Runner, question, previousID string) tea.Cmd {
	return func() tea.Msg {
		answer, err := runner.SideQuestion(ctx, question, previousID)
		return btwDoneEvent{question: question, answer: answer, err: err}
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

func runExport(runner *agent.Runner, filename, cwd string) tea.Cmd {
	return func() tea.Msg {
		if runner == nil {
			return exportDoneEvent{err: errors.New("no active session to export")}
		}
		text, path, err := runner.ExportSession(filename, cwd)
		return exportDoneEvent{text: text, path: path, err: err}
	}
}

func runSyntheticTurn(ctx context.Context, runner *agent.Runner, prompt, previousID string) tea.Cmd {
	return func() tea.Msg {
		result, err := runner.RunSyntheticTurn(ctx, prompt, previousID)
		return turnDoneEvent{result: result, err: err}
	}
}

func (m *model) startNext() tea.Cmd {
	if m.running {
		return nil
	}
	if len(m.pendingPrompts) > 0 {
		prompt := m.pendingPrompts[0]
		m.pendingPrompts = m.pendingPrompts[1:]
		m.running = true
		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		m.beginTurn(prompt)
		return runTurn(turnCtx, m.runner, prompt, m.previousID)
	}
	if len(m.scheduled) == 0 {
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
	frameStarted := time.Now()
	defer func() { m.debug.recordFrame(time.Since(frameStarted)) }()
	width := max(m.width, 20)
	colors := m.colors()
	mode := ""
	if m.planMode {
		mode = "  " + colors.warning + "\x1b[7m PLAN " + ansiReset
	}
	if m.bridge != nil {
		switch m.bridge.PermissionMode() {
		case tools.PermissionAuto:
			mode += "  " + colors.modal + "\x1b[7m AUTO " + ansiReset
		case tools.PermissionAlwaysApprove:
			mode += "  " + colors.error + "\x1b[7m ALWAYS " + ansiReset
		}
	}
	if m.scrollFocused {
		mode += "  " + colors.heading + "\x1b[7m SCROLLBACK " + ansiReset
	}
	header := fmt.Sprintf("%s%s Gork Go%s%s  %s%s · %s%s", ansiBold, colors.title, ansiReset, mode, ansiDim, truncate(m.modelName, 24), truncate(m.workspace, max(width-45, 10)), ansiReset)
	header = padRight(truncateANSIUnsafe(header, width), width)
	banner := m.announcementBanner(width)
	content := m.transcriptText()
	if m.planReview != nil {
		content = "# Review implementation plan\n\n" + m.planReview.event.event.PlanContent
	} else if m.mcp != nil {
		content = m.mcpContent()
	} else if m.claudeImport != nil {
		content = m.claudeImportContent()
	} else if m.extensions != nil {
		content = m.extensionsContent()
	} else if m.agentConfig != nil {
		content = m.agentConfigContent()
	} else if m.dashboard != nil {
		content = m.dashboardContent()
	} else if m.settings != nil {
		content = m.settingsContent()
	} else if m.docs != nil {
		content = m.docsContent()
	} else if m.sessionSelect != nil {
		content = m.sessionSelectContent()
	} else if m.forkChoice != nil {
		content = m.forkChoiceContent()
	} else if m.modelSelect != nil {
		content = m.modelSelectContent()
	} else if m.rewind != nil {
		content = m.rewindContent()
	} else if m.viewer != nil {
		content = m.viewerContent()
	} else if m.remember != nil {
		label, note := "Raw", m.remember.raw
		if m.remember.showEnhanced && m.remember.enhanced != "" {
			label, note = "Enhanced", m.remember.enhanced
		}
		content = "# Memory Note\n\n**" + label + "**\n\n" + note
	}
	contentLines := renderMarkdownTheme(content, m.transcriptRenderWidth(), m.hyperlinks, m.colors())
	if m.historySearch != nil {
		contentLines = m.historySearchLines(m.transcriptRenderWidth(), m.contentHeight())
	} else if m.scrollSearch != nil {
		contentLines = m.scrollSearch.highlighted(contentLines)
	}
	transcriptLineCount := len(contentLines)
	if m.transcriptVisible() && m.scrollTail > 0 {
		contentLines = append(contentLines, make([]string, m.scrollTail)...)
	}
	timelineRail := m.computeTimelineRail(transcriptLineCount)
	visible := sliceFromBottom(contentLines, m.contentHeight(), m.scroll)
	for len(visible) < m.contentHeight() {
		visible = append(visible, "")
	}
	if m.jump != nil {
		visible = m.jumpOverlay(visible, width)
	}
	visible = m.renderTimeline(visible, timelineRail)
	visible = m.debug.overlay(visible, width, m.scroll, m.maxTranscriptScroll(), m.contentHeight(), transcriptLineCount, m.scrollLines, m.invertScroll, m.scrollFocused)
	plainVisible := make([]string, len(visible))
	for index, line := range visible {
		plainVisible[index] = stripUIANSI(line)
	}
	if m.selection != nil {
		visible = m.selection.highlightedLines(visible)
	}
	body := strings.Join(visible, "\n")

	var footer string
	if m.approval != nil {
		footer = fmt.Sprintf("%s%sApprove %s?%s %s\n%s[y] allow  [n/esc] deny%s", ansiBold, colors.modal, m.approval.action, ansiReset, truncate(m.approval.detail, width-20), ansiDim, ansiReset)
	} else if m.planReview != nil {
		if m.planReview.editing {
			footer = fmt.Sprintf("%s%s%s%s\n> %s\n%s%s%s", ansiBold, colors.modal, truncate("Request plan changes", width), ansiReset, renderInput(m.input, m.cursor, max(width-2, 1)), ansiDim, truncate("Enter send · Esc back · Ctrl-U clear", width), ansiReset)
		} else {
			footer = ansiBold + colors.modal + "Plan review" + ansiReset + "\n" + ansiDim + truncate("[Y] approve · [R] request changes · [A] abandon · Esc keep planning", width) + ansiReset
		}
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
		footer = fmt.Sprintf("%s%s%s%s\n%s\n> %s\n%s%s%s", ansiBold, colors.modal,
			truncate(question.Question, width), ansiReset, truncate(strings.Join(labels, "  ")+"  [Other] type response", width),
			renderInput(m.input, m.cursor, max(width-2, 1)), ansiDim, truncate(hint, width), ansiReset)
	} else if m.mcp != nil {
		footer = ansiBold + colors.modal + "MCP servers" + ansiReset + "\n" + ansiDim + truncate(m.mcpHint(), width) + ansiReset
	} else if m.claudeImport != nil {
		footer = ansiBold + colors.modal + "Import Claude settings" + ansiReset + "\n" + ansiDim + truncate("Up/Down select | Space toggle | A all | N none | Enter import | Esc cancel", width) + ansiReset
	} else if m.extensions != nil {
		footer = ansiBold + colors.modal + "Extensions" + ansiReset + "\n" + ansiDim + truncate(m.extensionsHint(), width) + ansiReset
	} else if m.agentConfig != nil {
		footer = ansiBold + colors.modal + "Agents and personas" + ansiReset + "\n" + ansiDim + truncate(m.agentConfigHint(), width) + ansiReset
	} else if m.dashboard != nil {
		footer = ansiBold + colors.modal + "Agent Dashboard" + ansiReset + "\n" + ansiDim + truncate(m.dashboardHint(), width) + ansiReset
	} else if m.settings != nil {
		footer = ansiBold + colors.modal + "Settings" + ansiReset + "\n" + ansiDim + truncate("Up/Down select | Enter/Space change | Esc close", width) + ansiReset
	} else if m.docs != nil {
		footer = ansiBold + colors.modal + "Docs" + ansiReset + "\n" + ansiDim + truncate(m.docsHint(), width) + ansiReset
	} else if m.sessionSelect != nil {
		footer = ansiBold + colors.modal + "Resume session" + ansiReset + "\n> " + renderInput(m.sessionSelect.query, m.sessionSelect.cursor, max(width-2, 1)) + "\n" + ansiDim + truncate(m.sessionSelectHint(), width) + ansiReset
	} else if m.forkChoice != nil {
		footer = ansiBold + colors.modal + "Fork session" + ansiReset + "\n" + ansiDim + "Up/Down select · Enter confirm · Y/N choose · Esc cancel" + ansiReset
	} else if m.modelSelect != nil {
		footer = ansiBold + colors.modal + "Model" + ansiReset + "\n" + ansiDim + truncate(m.modelSelectHint(), width) + ansiReset
	} else if m.rewind != nil {
		footer = ansiBold + colors.modal + "Rewind" + ansiReset + "\n" + ansiDim + truncate(m.rewindHint(), width) + ansiReset
	} else if m.jump != nil {
		footer = ansiBold + colors.modal + "Jump" + ansiReset + "\n" + ansiDim + truncate("Up/Down or j/k select · Enter jump · Esc restore", width) + ansiReset
	} else if m.remember != nil {
		tab := "enhancing..."
		if m.remember.enhanceDone {
			tab = "raw only"
		}
		if m.remember.enhanced != "" {
			tab = "Tab raw/enhanced"
		}
		footer = ansiBold + colors.positive + "Memory note review" + ansiReset + "\n" + ansiDim + truncate("Enter/Y save · "+tab+" · Esc cancel", width) + ansiReset
	} else if m.rememberInput {
		footer = ansiBold + colors.positive + "# Save a memory note" + ansiReset + "\n> " + renderInput(m.input, m.cursor, max(width-2, 1)) + "\n" + ansiDim + "Enter review · Esc cancel" + ansiReset
	} else if m.historySearch != nil {
		footer = ansiBold + colors.positive + "Prompt history" + ansiReset + "\n> " + renderInput(m.input, m.cursor, max(width-2, 1)) + "\n" + ansiDim + "Enter/Tab restore · Esc cancel · Up/Down select" + ansiReset
	} else if m.scrollSearch != nil {
		footer = ansiBold + colors.positive + "Search scrollback" + ansiReset + "\n> " + renderInput(m.scrollSearch.query, m.scrollSearch.cursor, max(width-2, 1)) + "\n" + ansiDim + truncate(m.scrollSearch.status(), width) + ansiReset
	} else {
		inputLines := []string{"> "}
		if m.running {
			inputLines = []string{"> "}
		} else {
			inputLines = renderPromptInputWithGhost(m.input, m.cursor, m.promptSuggestionGhost(), width, m.visiblePromptInputRows())
			if m.feedbackInput && len(inputLines) > 0 {
				inputLines[0] = "~ " + strings.TrimPrefix(inputLines[0], "> ")
			}
		}
		hint := "Enter send · Shift/Alt-Enter newline · Ctrl-M multiline · Ctrl-Z undo"
		if m.multiline {
			hint = "Shift/Alt-Enter send · Enter newline · Ctrl-M single-line · Ctrl-Z undo"
		}
		footer = strings.Join(inputLines, "\n") + "\n" + ansiDim + truncate(hint, width) + ansiReset
	}
	status := ansiDim + truncate(m.status, width) + ansiReset
	prefix := header + "\n"
	if len(banner) > 0 {
		prefix += strings.Join(banner, "\n") + "\n"
	}
	view := tea.NewView(prefix + body + status + "\n" + footer)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	if !m.mouseReleased {
		view.MouseMode = tea.MouseModeCellMotion
	}
	contentHeight := m.contentHeight()
	bannerHeight := len(banner)
	bodyEnd := bannerHeight + contentHeight
	view.OnMouse = func(message tea.MouseMsg) tea.Cmd {
		mouse := message.Mouse()
		bodyRow := mouse.Y - bannerHeight - 1
		switch message.(type) {
		case tea.MouseWheelMsg:
			if mouse.Y < bannerHeight+1 || mouse.Y > bodyEnd {
				return nil
			}
			scrollLines := m.scrollLines
			if scrollLines == 0 {
				scrollLines = mouseWheelScrollLines
			}
			if m.invertScroll {
				scrollLines = -scrollLines
			}
			lines := 0
			switch mouse.Button {
			case tea.MouseWheelUp:
				lines = scrollLines
			case tea.MouseWheelDown:
				lines = -scrollLines
			default:
				return nil
			}
			return func() tea.Msg { return mouseScrollEvent{lines: lines} }
		case tea.MouseClickMsg:
			if mouse.Button != tea.MouseLeft {
				return nil
			}
			if m.jump == nil && timelineRail != nil {
				if hit := timelineRail.hit(mouse.X, bodyRow); hit != nil {
					if hit.turn >= 0 {
						return func() tea.Msg { return timelineJumpEvent{turn: hit.turn} }
					}
					return nil
				}
			}
			if mouse.Y >= bannerHeight+1 && mouse.Y <= bodyEnd {
				adjusted := mouse
				adjusted.Y -= bannerHeight
				event := mouseSelectionEvent{phase: selectionStart, point: selectionPointForMouse(adjusted, plainVisible), lines: plainVisible, at: time.Now()}
				return func() tea.Msg { return event }
			}
			if mouse.Y < bodyEnd+3 {
				return nil
			}
			if event, ok := m.footerClick(mouse.X, mouse.Y, width); ok {
				return func() tea.Msg { return event }
			}
		case tea.MouseMotionMsg:
			if mouse.Button == tea.MouseLeft && m.selection != nil {
				adjusted := mouse
				adjusted.Y -= bannerHeight
				event := mouseSelectionEvent{phase: selectionMove, point: selectionPointForMouse(adjusted, m.selection.lines)}
				return func() tea.Msg { return event }
			}
			if m.jump == nil && timelineRail != nil {
				hit := timelineRail.hit(mouse.X, bodyRow)
				if hit == nil || hit.kind != timelineTick {
					hit = nil
				}
				if !timelineHitsEqual(m.timelineHover, hit) {
					return func() tea.Msg { return timelineHoverEvent{hit: hit} }
				}
			}
		case tea.MouseReleaseMsg:
			if (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) && m.selection != nil {
				adjusted := mouse
				adjusted.Y -= bannerHeight
				event := mouseSelectionEvent{phase: selectionRelease, point: selectionPointForMouse(adjusted, m.selection.lines)}
				return func() tea.Msg { return event }
			}
		}
		return nil
	}
	return view
}

func (m *model) announcementBanner(width int) []string {
	if m.runner == nil || m.runner.Announcements == nil {
		return nil
	}
	item, ok := m.runner.Announcements.Current()
	if !ok {
		return nil
	}
	value := func(text *string) string {
		if text == nil {
			return ""
		}
		return strings.TrimSpace(sanitizeTerminalText(*text))
	}
	dismissible := item.Dismissible == nil || *item.Dismissible
	hide := ""
	if dismissible {
		hide = "[hide]"
	}
	if value(item.Severity) == "critical" {
		colors := m.colors()
		title := value(item.Title)
		if title == "" {
			title = "Announcement"
		}
		first := fitAnnouncementParts("! "+title, hide, width)
		second := value(item.Message)
		if dismissible {
			second = fitAnnouncementParts(second, "hide: /announcements hide", width)
		} else {
			second = truncate(second, width)
		}
		return []string{ansiBold + colors.error + first + ansiReset, second}
	}
	left := value(item.Message)
	button, target := "", ""
	if item.CTA != nil && value(item.CTA.Label) != "" && safeAnnouncementURL(value(item.CTA.URL)) {
		button, target = "["+value(item.CTA.Label)+"]", value(item.CTA.URL)
		left = button
		if !dismissible && value(item.CTA.Caption) != "" {
			left += " " + value(item.CTA.Caption)
		}
	}
	right := ""
	if dismissible {
		right = "hide: /announcements hide  [hide]"
	}
	line := fitAnnouncementParts(left, right, width)
	if button != "" && target != "" && m.hyperlinks && strings.Contains(line, button) {
		link := ansi.SetHyperlink(target, "id="+hyperlinkID(target)) + button + ansi.ResetHyperlink()
		line = strings.Replace(line, button, link, 1)
	}
	return []string{m.colors().warning + line + ansiReset}
}

func (m *model) pinnedAnnouncementURL() string {
	if m.runner == nil || m.runner.Announcements == nil {
		return ""
	}
	item, ok := m.runner.Announcements.Current()
	if !ok || item.Severity == nil || strings.TrimSpace(*item.Severity) != "promo" || item.Dismissible == nil || *item.Dismissible || item.CTA == nil || item.CTA.URL == nil {
		return ""
	}
	target := strings.TrimSpace(*item.CTA.URL)
	if !safeAnnouncementURL(target) {
		return ""
	}
	return target
}

func fitAnnouncementParts(left, right string, width int) string {
	if right == "" {
		return truncateAnnouncement(left, width)
	}
	rightWidth := displayWidth(right)
	if width <= rightWidth+2 {
		return truncateAnnouncement(right, width)
	}
	left = truncateAnnouncement(left, width-rightWidth-2)
	return left + strings.Repeat(" ", max(width-displayWidth(left)-rightWidth, 2)) + right
}

func truncateAnnouncement(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	used := 0
	var result strings.Builder
	for _, char := range value {
		charWidth := runeWidth(char)
		if used+charWidth > width-1 {
			break
		}
		result.WriteRune(char)
		used += charWidth
	}
	return result.String() + "…"
}

func safeAnnouncementURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func (m *model) viewerContent() string {
	if m.viewer == nil {
		return ""
	}
	title := m.viewer.title
	if m.showTimestamps && !m.viewer.at.IsZero() {
		title += "  " + m.viewer.at.Local().Format("3:04 PM")
	}
	return "# " + title + "\n\n" + strings.TrimSpace(m.viewer.content)
}

func (m *model) modelSelectContent() string {
	state := m.modelSelect
	if state == nil {
		return ""
	}
	if state.phase == modelSelectError {
		return "# Model switch failed\n\n" + state.err
	}
	if state.phase == modelSelectEffort {
		lines := make([]string, 0, len(state.efforts))
		for _, effort := range state.efforts {
			label := effort.Label
			if label == "" {
				label = effort.ID
			}
			suffix := ""
			if state.model.ID == m.runner.ModelID && matchesEffort(effort, m.runner.ReasoningEffort) {
				suffix = " (current)"
			} else if state.model.ID != m.runner.ModelID && (effort.Default || matchesEffort(effort, state.model.ReasoningEffort)) {
				suffix = " (default)"
			}
			lines = append(lines, fmt.Sprintf("%s%s · %s", label, suffix, effort.ID))
		}
		name := state.model.Name
		if name == "" {
			name = state.model.Model
		}
		return fmt.Sprintf("# Reasoning effort for %s\n\n%s", name, selectedLines(lines, state.selected))
	}
	lines := make([]string, 0, len(state.models))
	for _, option := range state.models {
		name := option.Name
		if name == "" {
			name = option.Model
		}
		suffix := ""
		if option.ID == m.runner.ModelID {
			suffix = " (current)"
		}
		line := name + suffix
		if option.Description != "" {
			line += " · " + strings.Join(strings.Fields(option.Description), " ")
		}
		lines = append(lines, line)
	}
	return "# Select model\n\n" + selectedWindow(lines, state.selected, max(m.contentHeight()-4, 1))
}

func (m *model) modelSelectHint() string {
	if m.modelSelect != nil && m.modelSelect.phase == modelSelectError {
		return "Enter/Esc close"
	}
	if m.modelSelect != nil && m.modelSelect.phase == modelSelectEffort && !m.modelSelect.effortOnly {
		return "Up/Down select · Enter switch · Esc models"
	}
	return "Up/Down select · Enter switch · Esc cancel"
}

func (m *model) rewindContent() string {
	state := m.rewind
	if state == nil {
		return ""
	}
	switch state.phase {
	case rewindLoading:
		return "# Rewind\n\nLoading turns..."
	case rewindCancelOffer:
		options := []string{"Stop the current turn and rewind", "Keep the current turn running"}
		return "# Rewind\n\nA turn is still running.\n\n" + selectedLines(options, state.selected)
	case rewindCancelling:
		return "# Rewind\n\nCancelling the current turn..."
	case rewindPicker:
		lines := make([]string, 0, len(state.points))
		for _, point := range state.points {
			preview := "(empty prompt)"
			if point.PromptPreview != nil {
				preview = strings.ReplaceAll(*point.PromptPreview, "\n", " ")
			}
			files := ""
			if point.NumFileSnapshots > 0 {
				files = fmt.Sprintf(" · %d file(s)", point.NumFileSnapshots)
			}
			lines = append(lines, fmt.Sprintf("Turn %d%s · %s", point.PromptIndex+1, files, preview))
		}
		return "# Select a turn\n\n" + selectedWindow(lines, state.selected, max(m.contentHeight()-4, 1))
	case rewindModeSelect:
		modes := rewindModes(state.points, state.target)
		labels := make([]string, 0, len(modes))
		for _, mode := range modes {
			labels = append(labels, strings.ReplaceAll(string(mode), "_", " "))
		}
		return fmt.Sprintf("# Rewind turn %d\n\nChoose what to restore.\n\n%s", state.target+1, selectedLines(labels, state.selected))
	case rewindPreviewing:
		return "# Rewind\n\nChecking file changes..."
	case rewindConfirm:
		var details strings.Builder
		fmt.Fprintf(&details, "# Confirm rewind\n\nRewind %s to turn %d?", strings.ReplaceAll(string(state.mode), "_", " "), state.target+1)
		if len(state.preview.CleanFiles) > 0 {
			fmt.Fprintf(&details, "\n\nFiles to restore:\n%s", bulletLines(state.preview.CleanFiles))
		}
		if len(state.preview.Conflicts) > 0 {
			details.WriteString("\n\nExternal changes will be overwritten:\n")
			for _, conflict := range state.preview.Conflicts {
				fmt.Fprintf(&details, "- %s (%s)\n", conflict.Path, strings.ReplaceAll(conflict.ConflictType, "_", " "))
			}
		}
		return strings.TrimSpace(details.String())
	case rewindExecuting:
		return "# Rewind\n\nRestoring the selected state..."
	case rewindError:
		return "# Rewind failed\n\n" + state.err
	default:
		return "# Rewind"
	}
}

func (m *model) rewindHint() string {
	switch m.rewind.phase {
	case rewindPicker, rewindModeSelect:
		return "Up/Down select · Enter continue · Esc back"
	case rewindCancelOffer:
		return "Y stop and rewind · N keep running · Esc cancel"
	case rewindConfirm:
		return "Y/Enter confirm · N/Esc back"
	case rewindError:
		return "Enter/Esc close"
	default:
		return "Esc cancel"
	}
}

func selectedLines(lines []string, selected int) string {
	var result strings.Builder
	for index, line := range lines {
		prefix := "  "
		if index == selected {
			prefix = "> "
		}
		result.WriteString(prefix + line + "\n")
	}
	return strings.TrimSpace(result.String())
}

func selectedWindow(lines []string, selected, size int) string {
	size = max(size, 1)
	start := max(0, selected-size/2)
	start = min(start, max(len(lines)-size, 0))
	end := min(len(lines), start+size)
	return selectedLines(lines[start:end], selected-start)
}

func bulletLines(lines []string) string {
	var result strings.Builder
	for _, line := range lines {
		result.WriteString("- " + line + "\n")
	}
	return strings.TrimSpace(result.String())
}

func (m *model) maxViewerScroll() int {
	return max(len(renderMarkdownTheme(m.viewerContent(), max(m.width, 20), false, m.colors()))-m.contentHeight(), 0)
}

func (m *model) footerClick(x, y, width int) (mouseClickEvent, bool) {
	if y != m.contentHeight()+m.announcementHeight()+3 {
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
	banner := m.announcementHeight()
	if m.question != nil || m.planReview != nil || m.remember != nil || m.rememberInput || m.rewind != nil || m.jump != nil || m.modelSelect != nil || m.settings != nil || m.docs != nil || m.sessionSelect != nil || m.forkChoice != nil || m.mcp != nil || m.claudeImport != nil || m.extensions != nil || m.agentConfig != nil || m.dashboard != nil {
		return max(m.height-7-banner, 3)
	}
	if m.historySearch != nil {
		return max(m.height-6-banner, 3)
	}
	if m.scrollSearch != nil {
		return max(m.height-6-banner, 3)
	}
	rows := 1
	if !m.running {
		rows = min(strings.Count(string(m.input), "\n")+1, m.visiblePromptInputRows())
	}
	return max(m.height-4-rows-banner, 3)
}

func (m *model) announcementHeight() int {
	if m.runner == nil || m.runner.Announcements == nil {
		return 0
	}
	item, ok := m.runner.Announcements.Current()
	if !ok {
		return 0
	}
	if item.Severity != nil && strings.TrimSpace(*item.Severity) == "critical" {
		return 2
	}
	return 1
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

func renderPromptInputWithGhost(input []rune, cursor int, ghost string, width, maxRows int) []string {
	lines := renderPromptInput(input, cursor, width, maxRows)
	if ghost == "" || cursor != len(input) || len(lines) == 0 {
		return lines
	}
	remaining := width - displayWidth(stripUIANSI(lines[len(lines)-1]))
	if remaining > 0 {
		lines[len(lines)-1] += "\x1b[2m" + fitInputLine([]rune(ghost), remaining) + "\x1b[0m"
	}
	return lines
}

func (m *model) promptSuggestionGhost() string {
	if !m.suggestionsEnabled || m.suggestionDismissed || m.cursor != len(m.input) {
		return ""
	}
	remainder, ok := strings.CutPrefix(m.promptSuggestion, string(m.input))
	if !ok || remainder == "" {
		return ""
	}
	return remainder
}

func (m *model) clearPromptSuggestion() {
	m.promptSuggestion = ""
	m.suggestionDismissed = false
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
