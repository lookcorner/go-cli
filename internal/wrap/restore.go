package wrap

import "sync"

const (
	modeMouse1000    uint32 = 1 << 0
	modeMouse1002    uint32 = 1 << 1
	modeMouse1003    uint32 = 1 << 2
	modeMouse1005    uint32 = 1 << 3
	modeMouse1015    uint32 = 1 << 4
	modeMouse1016    uint32 = 1 << 5
	modeMouse1006    uint32 = 1 << 6
	modePaste2004    uint32 = 1 << 7
	modeFocus1004    uint32 = 1 << 8
	modeSync2026     uint32 = 1 << 9
	modeAlt47        uint32 = 1 << 10
	modeAlt1047      uint32 = 1 << 11
	modeAlt1049      uint32 = 1 << 12
	modeCursorHidden uint32 = 1 << 13
)

var disableOrder = []struct {
	bit uint32
	seq []byte
}{
	{modeMouse1000, []byte("\x1b[?1000l")},
	{modeMouse1002, []byte("\x1b[?1002l")},
	{modeMouse1003, []byte("\x1b[?1003l")},
	{modeMouse1005, []byte("\x1b[?1005l")},
	{modeMouse1015, []byte("\x1b[?1015l")},
	{modeMouse1016, []byte("\x1b[?1016l")},
	{modeMouse1006, []byte("\x1b[?1006l")},
	{modePaste2004, []byte("\x1b[?2004l")},
	{modeFocus1004, []byte("\x1b[?1004l")},
}

// ModeTracker observes child→terminal CSI and remembers latched DEC private
// modes plus kitty keyboard push depth so wrap can repair dirty exits.
type ModeTracker struct {
	mu          sync.Mutex
	modes       uint32
	kittyDepth  uint32
	restoreOnce sync.Once
}

// ModeSnapshot is a point-in-time copy of latched wrap terminal state.
type ModeSnapshot struct {
	Modes      uint32
	KittyDepth uint32
}

func NewModeTracker() *ModeTracker {
	return &ModeTracker{}
}

// ObserveCSI updates latched state from one complete CSI sequence (ESC [ … final).
func (t *ModeTracker) ObserveCSI(seq []byte) {
	if t == nil || len(seq) < 3 {
		return
	}
	finalByte := seq[len(seq)-1]
	body := seq[2 : len(seq)-1]
	switch finalByte {
	case 'h', 'l':
		if len(body) == 0 || body[0] != '?' {
			return
		}
		set := finalByte == 'h'
		for _, param := range splitCSIParams(body[1:]) {
			if mode, ok := parseDecimal(param); ok {
				t.applyDECMode(mode, set)
			}
		}
	case 'u':
		if len(body) == 0 {
			return
		}
		switch body[0] {
		case '>':
			t.mu.Lock()
			t.kittyDepth++
			t.mu.Unlock()
		case '<':
			n := uint32(1)
			if parsed, ok := parseDecimal(body[1:]); ok && parsed > 0 {
				n = parsed
			}
			t.mu.Lock()
			if t.kittyDepth > n {
				t.kittyDepth -= n
			} else {
				t.kittyDepth = 0
			}
			t.mu.Unlock()
		}
	}
}

func (t *ModeTracker) applyDECMode(mode uint32, set bool) {
	bit, ok := modeBit(mode)
	if !ok {
		return
	}
	latch := set
	if mode == 25 {
		latch = !set
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if latch {
		t.modes |= bit
	} else {
		t.modes &^= bit
	}
}

func (t *ModeTracker) Snapshot() ModeSnapshot {
	if t == nil {
		return ModeSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return ModeSnapshot{Modes: t.modes, KittyDepth: t.kittyDepth}
}

// ClaimRestore returns true once for the first exit path that should emit restores.
func (t *ModeTracker) ClaimRestore() bool {
	if t == nil {
		return false
	}
	claimed := false
	t.restoreOnce.Do(func() { claimed = true })
	return claimed
}

// RestoreBytes returns disable sequences for exactly the latched state.
// Nothing latched yields nil so clean exits stay byte-transparent.
func RestoreBytes(snapshot ModeSnapshot) []byte {
	var out []byte
	if snapshot.Modes&modeSync2026 != 0 {
		out = append(out, []byte("\x1b[?2026l")...)
	}
	if snapshot.Modes&modeCursorHidden != 0 {
		out = append(out, []byte("\x1b[?25h")...)
	}
	for _, item := range disableOrder {
		if snapshot.Modes&item.bit != 0 {
			out = append(out, item.seq...)
		}
	}
	for i := uint32(0); i < snapshot.KittyDepth; i++ {
		out = append(out, []byte("\x1b[<u")...)
	}
	if snapshot.Modes&modeAlt1047 != 0 {
		out = append(out, []byte("\x1b[?1047l")...)
	}
	if snapshot.Modes&modeAlt47 != 0 {
		out = append(out, []byte("\x1b[?47l")...)
	}
	if snapshot.Modes&modeAlt1049 != 0 {
		out = append(out, []byte("\x1b[?1049l")...)
	}
	return out
}

func modeBit(mode uint32) (uint32, bool) {
	switch mode {
	case 25:
		return modeCursorHidden, true
	case 47:
		return modeAlt47, true
	case 1000:
		return modeMouse1000, true
	case 1002:
		return modeMouse1002, true
	case 1003:
		return modeMouse1003, true
	case 1004:
		return modeFocus1004, true
	case 1005:
		return modeMouse1005, true
	case 1006:
		return modeMouse1006, true
	case 1015:
		return modeMouse1015, true
	case 1016:
		return modeMouse1016, true
	case 1047:
		return modeAlt1047, true
	case 1049:
		return modeAlt1049, true
	case 2004:
		return modePaste2004, true
	case 2026:
		return modeSync2026, true
	default:
		return 0, false
	}
}

func splitCSIParams(params []byte) [][]byte {
	if len(params) == 0 {
		return nil
	}
	var parts [][]byte
	start := 0
	for index, value := range params {
		if value == ';' {
			parts = append(parts, params[start:index])
			start = index + 1
		}
	}
	parts = append(parts, params[start:])
	return parts
}

func parseDecimal(bytes []byte) (uint32, bool) {
	if len(bytes) == 0 {
		return 0, false
	}
	var value uint32
	for _, b := range bytes {
		if b < '0' || b > '9' {
			return 0, false
		}
		next := value*10 + uint32(b-'0')
		if next < value {
			return 0, false
		}
		value = next
	}
	return value, true
}
