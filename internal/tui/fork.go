package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

type ForkResult struct {
	Path      string
	Workspace string
}

type ForkSessionError struct {
	Path      string
	Workspace string
	Directive string
}

func (e *ForkSessionError) Error() string { return "fork session " + e.Path }

type forkArgs struct {
	worktree  *bool
	target    *int
	directive string
}

func parseForkArgs(value string) (forkArgs, error) {
	rest := strings.TrimLeft(value, " \t\r\n")
	var worktree *bool
	var target *int
	for rest != "" {
		separator := strings.IndexFunc(rest, unicode.IsSpace)
		flag, after := rest, ""
		if separator >= 0 {
			flag, after = rest[:separator], rest[separator:]
		}
		switch flag {
		case "--worktree":
			if worktree != nil {
				if *worktree {
					return forkArgs{}, errors.New("--worktree specified twice")
				}
				return forkArgs{}, errors.New("--worktree and --no-worktree are mutually exclusive")
			}
			selected := true
			worktree = &selected
		case "--no-worktree":
			if worktree != nil {
				if !*worktree {
					return forkArgs{}, errors.New("--no-worktree specified twice")
				}
				return forkArgs{}, errors.New("--worktree and --no-worktree are mutually exclusive")
			}
			selected := false
			worktree = &selected
		case "--at":
			if target != nil {
				return forkArgs{}, errors.New("--at specified twice")
			}
			after = strings.TrimLeft(after, " \t\r\n")
			if after == "" {
				return forkArgs{}, errors.New("--at requires a prompt index")
			}
			separator = strings.IndexFunc(after, unicode.IsSpace)
			value, remaining := after, ""
			if separator >= 0 {
				value, remaining = after[:separator], after[separator:]
			}
			parsed, err := parseForkTarget(value)
			if err != nil {
				return forkArgs{}, err
			}
			target = &parsed
			after = remaining
		default:
			if value, ok := strings.CutPrefix(flag, "--at="); ok {
				if target != nil {
					return forkArgs{}, errors.New("--at specified twice")
				}
				parsed, err := parseForkTarget(value)
				if err != nil {
					return forkArgs{}, err
				}
				target = &parsed
				break
			}
			return forkArgs{worktree: worktree, target: target, directive: rest}, nil
		}
		rest = strings.TrimLeft(after, " \t\r\n")
	}
	return forkArgs{worktree: worktree, target: target}, nil
}

func parseForkTarget(value string) (int, error) {
	target, err := strconv.Atoi(value)
	if err != nil || target < 0 {
		return 0, fmt.Errorf("invalid --at prompt index %q", value)
	}
	return target, nil
}

type forkChoiceState struct {
	directive string
	target    *int
	selected  int
}

type forkDoneEvent struct {
	result    ForkResult
	directive string
	err       error
}

func runFork(ctx context.Context, fork func(context.Context, bool, string, *int) (ForkResult, error), worktree bool, modelID, directive string, target *int) tea.Cmd {
	return func() tea.Msg {
		result, err := fork(ctx, worktree, modelID, target)
		return forkDoneEvent{result: result, directive: directive, err: err}
	}
}

func (m *model) startFork(worktree bool, directive string, target *int) tea.Cmd {
	if m.forkSession == nil {
		m.appendSystem("Forking is unavailable.")
		m.status = "fork unavailable"
		return nil
	}
	m.forkChoice = nil
	m.running = true
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.status = "creating fork"
	modelID := m.forkSecondaryModel
	if modelID == "" && m.runner != nil {
		modelID = m.runner.ModelID
	}
	return runFork(turnCtx, m.forkSession, worktree, modelID, directive, target)
}

func (m *model) handleForkChoiceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, stroke := msg.Key(), strings.ToLower(msg.Keystroke())
	switch {
	case stroke == "esc" || stroke == "ctrl+c":
		m.forkChoice = nil
		m.status = "fork cancelled"
	case stroke == "y":
		return m, m.startFork(true, m.forkChoice.directive, m.forkChoice.target)
	case stroke == "n":
		return m, m.startFork(false, m.forkChoice.directive, m.forkChoice.target)
	case key.Code == tea.KeyUp || key.Code == tea.KeyDown || key.Code == tea.KeyTab:
		m.forkChoice.selected = 1 - m.forkChoice.selected
	case key.Code == tea.KeyEnter:
		return m, m.startFork(m.forkChoice.selected == 0, m.forkChoice.directive, m.forkChoice.target)
	}
	return m, nil
}

func (m *model) forkChoiceContent() string {
	options := []string{"Yes - isolated Git worktree", "No - current working directory"}
	var content strings.Builder
	content.WriteString("# Fork session\n\nRun this fork in an isolated Git worktree?\n\n")
	for index, option := range options {
		marker := "  "
		if index == m.forkChoice.selected {
			marker = "> "
		}
		content.WriteString(marker + option + "\n")
	}
	return content.String()
}
