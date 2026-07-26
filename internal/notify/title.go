package notify

import (
	"strconv"
	"strings"
	"time"
)

// Title items accepted in [ui.notifications.title].items.
const (
	TitleGrok           = "grok"
	TitleSpinner        = "spinner"
	TitleActivity       = "activity"
	TitleSessionName    = "session-name"
	TitleCwd            = "cwd"
	TitleModel          = "model"
	TitleTurnTimer      = "turn-timer"
	TitleActionRequired = "action-required"
)

// TitleItems lists every supported item in reference order.
var TitleItems = []string{TitleActionRequired, TitleSpinner, TitleActivity, TitleSessionName, TitleCwd, TitleModel, TitleTurnTimer, TitleGrok}

// TitleTick is the cadence the title animates at: one spinner frame per tick
// and a half-second blink phase every two ticks.
const TitleTick = 250 * time.Millisecond

const (
	titleProduct       = "gork"
	titleSeparator     = " - "
	titleActionLabel   = "⚠ Action Required"
	titleBlinkInterval = 2
)

var titleSpinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧'}

// TitleState is the session state a title is composed from.
type TitleState struct {
	SessionName       string
	Model             string
	Cwd               string
	Activity          string
	Busy              bool
	PendingPermission bool
	Focused           bool
	TurnElapsed       time.Duration
}

// TitleManager composes terminal titles and emits an escape only when the
// composed title changes.
type TitleManager struct {
	enabled bool
	items   []string
	last    string
	ticks   uint64
}

// NewTitleManager builds a manager over the configured item order.
func NewTitleManager(enabled bool, items []string) *TitleManager {
	return &TitleManager{enabled: enabled, items: items}
}

// Animating reports whether the title still changes on its own, so callers know
// when to keep a tick armed.
func (t *TitleManager) Animating(state TitleState) bool {
	if t == nil || !t.enabled {
		return false
	}
	for _, item := range t.items {
		switch item {
		case TitleSpinner, TitleActivity, TitleTurnTimer:
			if state.Busy {
				return true
			}
		case TitleActionRequired:
			if state.PendingPermission && !state.Focused {
				return true
			}
		}
	}
	return false
}

// Update advances the animation and returns the escape sequence to write, or ""
// when disabled or the title is unchanged.
func (t *TitleManager) Update(state TitleState) string {
	if t == nil || !t.enabled {
		return ""
	}
	composed := t.compose(state)
	t.ticks++
	if composed == t.last {
		return ""
	}
	t.last = composed
	return titleEscape(composed)
}

// Reset restores the plain product title when a session ends.
func (t *TitleManager) Reset() string {
	if t == nil || !t.enabled {
		return ""
	}
	t.last, t.ticks = titleProduct, 0
	return titleEscape(titleProduct)
}

// compose renders the configured items, falling back to the product name when
// no item contributes anything.
func (t *TitleManager) compose(state TitleState) string {
	var title strings.Builder
	for _, item := range t.items {
		part := t.itemText(item, state)
		if part == "" {
			continue
		}
		if title.Len() > 0 {
			title.WriteString(titleSeparator)
		}
		title.WriteString(part)
	}
	if title.Len() == 0 {
		return titleProduct
	}
	return title.String()
}

// itemText renders one item, returning "" when it contributes nothing.
func (t *TitleManager) itemText(item string, state TitleState) string {
	switch item {
	case TitleGrok:
		return titleProduct
	case TitleSpinner:
		if !state.Busy {
			return ""
		}
		return string(titleSpinner[int(t.ticks)%len(titleSpinner)])
	case TitleActivity:
		if activity := strings.TrimSpace(state.Activity); activity != "" {
			return activity
		}
		if state.Busy {
			return "Waiting"
		}
		return ""
	case TitleSessionName:
		return truncateTitle(state.SessionName, 40)
	case TitleModel:
		return truncateTitle(state.Model, 30)
	case TitleCwd:
		cwd := state.Cwd
		if index := strings.LastIndexAny(cwd, `/\`); index >= 0 {
			cwd = cwd[index+1:]
		}
		return truncateTitle(cwd, 30)
	case TitleTurnTimer:
		seconds := int64(state.TurnElapsed / time.Second)
		if seconds < 1 {
			return ""
		}
		return strconv.FormatInt(seconds, 10) + "s"
	case TitleActionRequired:
		// Blink for attention only while away; a focused user is already here.
		if !state.PendingPermission {
			return ""
		}
		if !state.Focused && (t.ticks/titleBlinkInterval)%2 == 1 {
			return ""
		}
		return titleActionLabel
	default:
		return ""
	}
}

// truncateTitle bounds a part by rune count, marking any cut with an ellipsis.
func truncateTitle(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

// titleEscape builds the OSC 0 sequence, dropping control characters so a
// remote-sourced title cannot terminate the sequence early or inject escapes.
func titleEscape(title string) string {
	var sanitized strings.Builder
	for _, char := range title {
		if char >= 0x20 && char != 0x7f {
			sanitized.WriteRune(char)
		}
	}
	return "\x1b]0;" + sanitized.String() + "\x07"
}
