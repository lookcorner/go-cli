package tui

import "time"

const (
	scrollStreamGap     = 80 * time.Millisecond
	trackpadEventWindow = 30 * time.Millisecond
	trackpadEventsTick  = 3.0
)

type scrollInput struct {
	mode      string
	last      time.Time
	direction int
	events    int
	serial    uint64
}

func (s *scrollInput) event(direction, lines int, at time.Time) (float64, uint64) {
	if at.IsZero() {
		at = time.Now()
	}
	pending := 0.0
	if !s.last.IsZero() && (at.Sub(s.last) > scrollStreamGap || direction != s.direction) {
		pending = autoScrollLines(s.direction, lines, s.events)
		s.events = 0
		s.serial++
	} else if s.last.IsZero() {
		s.serial++
	}
	s.last, s.direction = at, direction
	s.events++

	if s.mode == "auto" {
		return pending, s.serial
	}
	if s.mode != "trackpad" {
		return float64(direction * lines), 0
	}
	return trackpadLines(direction, lines, s.events), 0
}

func (s *scrollInput) flush(serial uint64, at time.Time, lines int) float64 {
	if serial != s.serial || s.events == 0 || at.Sub(s.last) < trackpadEventWindow {
		return 0
	}
	direction, events := s.direction, s.events
	s.events = 0
	return autoScrollLines(direction, lines, events)
}

func autoScrollLines(direction, lines, events int) float64 {
	if events == 1 {
		return float64(direction * lines)
	}
	total := 0.0
	for event := 1; event <= events; event++ {
		total += trackpadLines(direction, lines, event)
	}
	return total
}

func trackpadLines(direction, lines, events int) float64 {
	acceleration := 1.0
	if events > 3 {
		acceleration = min(1+float64(events-3)/6, 3)
	}
	return float64(direction*lines) * acceleration / trackpadEventsTick
}
