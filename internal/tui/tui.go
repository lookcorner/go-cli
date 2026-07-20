package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
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
type scheduledFiredEvent struct{ event tools.ScheduledTaskFired }
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
	ctx        context.Context
	runner     *agent.Runner
	bridge     *Bridge
	workspace  string
	modelName  string
	previousID string
	transcript strings.Builder
	input      []rune
	width      int
	height     int
	scroll     int
	running    bool
	status     string
	approval   *approvalEvent
	question   *questionState
	planMode   bool
	planReview *planReviewState
	turnCancel context.CancelFunc
	initial    string
	scheduled  []tools.ScheduledTaskFired
	activeTask string
}

type planReviewState struct {
	event   planReviewEvent
	editing bool
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
		status: "ready", initial: strings.TrimSpace(initialPrompt),
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
		m.transcript.WriteString(msg.text)
		m.scroll = 0
		return m, waitForBridge(m.bridge)
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
		m.input = nil
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
		m.input = nil
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
	if m.question != nil {
		return m.handleQuestionKey(msg)
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
	if m.running {
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
	switch key.Code {
	case tea.KeyEnter:
		prompt := strings.TrimSpace(string(m.input))
		if prompt == "" {
			return m, nil
		}
		m.input = nil
		m.running = true
		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		if prompt == "/compact" {
			m.status = "compacting context"
			return m, runCompact(turnCtx, m.runner, m.previousID)
		}
		prompt, _ = tools.ExpandLoopCommand(prompt)
		m.beginTurn(prompt)
		return m, runTurn(turnCtx, m.runner, prompt, m.previousID)
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	}
	if stroke == "ctrl+u" {
		m.input = nil
		return m, nil
	}
	if key.Text != "" && utf8.ValidString(key.Text) {
		m.input = append(m.input, []rune(key.Text)...)
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
			m.input = nil
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
		m.input = nil
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
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if stroke == "ctrl+u" {
			m.input = nil
		} else if key.Text != "" && utf8.ValidString(key.Text) {
			m.input = append(m.input, []rune(key.Text)...)
		}
	}
	return m, nil
}

func (m *model) finishPlanReview(decision tools.PlanModeDecision) {
	m.planReview.event.reply <- decision
	m.planReview = nil
	m.input = nil
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
		question := m.question.event.request.Questions[m.question.index]
		answers, annotation, err := tools.ParseUserQuestionAnswer(question, string(m.input))
		if err != nil {
			m.status = "invalid answer: " + err.Error()
			return m, nil
		}
		m.question.answers[question.Question] = answers
		m.question.partial[question.Question] = strings.Join(answers, ", ")
		if annotation.Preview != "" || annotation.Notes != "" {
			m.question.annotations[question.Question] = annotation
		}
		m.question.index++
		m.input = nil
		if m.question.index == len(m.question.event.request.Questions) {
			m.finishQuestion(tools.UserQuestionResponse{Outcome: "accepted", Answers: m.question.answers, Annotations: m.question.annotations})
		} else {
			m.status = fmt.Sprintf("question %d/%d", m.question.index+1, len(m.question.event.request.Questions))
		}
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if stroke == "ctrl+u" {
			m.input = nil
		} else if key.Text != "" && utf8.ValidString(key.Text) {
			m.input = append(m.input, []rune(key.Text)...)
		}
	}
	return m, nil
}

func (m *model) finishQuestion(response tools.UserQuestionResponse) {
	m.question.event.reply <- response
	m.question = nil
	m.input = nil
	m.status = "thinking"
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
	}
	contentLines := renderMarkdown(content, width)
	visible := sliceFromBottom(contentLines, m.contentHeight(), m.scroll)
	body := strings.Join(visible, "\n")
	for len(visible) < m.contentHeight() {
		body += "\n"
		visible = append(visible, "")
	}

	var footer string
	if m.planReview != nil {
		if m.planReview.editing {
			footer = fmt.Sprintf("\x1b[1;33m%s\x1b[0m\n> %s█\n\x1b[2m%s\x1b[0m", truncate("Request plan changes", width), truncateFromLeft(string(m.input), max(width-2, 1)), truncate("Enter send · Esc back · Ctrl-U clear", width))
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
		footer = fmt.Sprintf("\x1b[1;33m%s\x1b[0m\n%s\n> %s█\n\x1b[2m%s\x1b[0m",
			truncate(question.Question, width), truncate(strings.Join(labels, "  ")+"  [Other] type response", width),
			truncateFromLeft(string(m.input), max(width-2, 1)), truncate(hint, width))
	} else {
		input := string(m.input)
		if m.running {
			input = ""
		}
		prompt := "> " + input
		if !m.running {
			prompt += "█"
		}
		footer = truncateFromLeft(prompt, width) + "\n\x1b[2m" + truncate("Enter send · Shift-Tab mode · PgUp/PgDn scroll · Ctrl-C cancel/quit · Ctrl-Q quit", width) + "\x1b[0m"
	}
	status := "\x1b[2m" + truncate(m.status, width) + "\x1b[0m"
	view := tea.NewView(header + "\n" + body + status + "\n" + footer)
	view.AltScreen = true
	return view
}

func (m *model) contentHeight() int {
	if m.question != nil || m.planReview != nil {
		return max(m.height-7, 3)
	}
	return max(m.height-5, 3)
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

func truncateFromLeft(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-width+1:])
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
