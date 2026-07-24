package wrap

import (
	"encoding/base64"
)

const (
	maxEscapeBytes   = 1 << 20
	maxClipboardSize = 768 << 10
)

type filterState uint8

const (
	filterNormal filterState = iota
	filterEscape
	filterOSC
	filterOSCEscape
	filterDCS
	filterTmuxOSC
	filterTmuxEscape
)

var tmuxPrefix = []byte("tmux;\x1b\x1b]")

// Filter removes valid OSC 52 writes from a byte stream and forwards their
// decoded payload to the local clipboard sink.
type Filter struct {
	state filterState
	buf   []byte
	sink  func([]byte)
}

func NewFilter(sink func([]byte)) *Filter {
	if sink == nil {
		sink = func([]byte) {}
	}
	return &Filter{sink: sink}
}

func (f *Filter) Feed(data []byte) []byte {
	output := make([]byte, 0, len(data))
	for _, value := range data {
		switch f.state {
		case filterNormal:
			if value == 0x1b {
				f.state = filterEscape
				f.buf = append(f.buf[:0], value)
			} else {
				output = append(output, value)
			}
		case filterEscape:
			f.buf = append(f.buf, value)
			switch value {
			case ']':
				f.state = filterOSC
			case 'P':
				f.state = filterDCS
			default:
				output = f.flushTo(output)
			}
		case filterOSC:
			f.buf = append(f.buf, value)
			switch value {
			case 0x07:
				output = f.finishOSC(output)
			case 0x1b:
				f.state = filterOSCEscape
			}
		case filterOSCEscape:
			f.buf = append(f.buf, value)
			if value == '\\' {
				output = f.finishOSC(output)
			} else {
				f.state = filterOSC
			}
		case filterDCS:
			f.buf = append(f.buf, value)
			offset := len(f.buf) - 3
			if offset < 0 || offset >= len(tmuxPrefix) || value != tmuxPrefix[offset] {
				output = f.flushTo(output)
			} else if offset == len(tmuxPrefix)-1 {
				f.state = filterTmuxOSC
			}
		case filterTmuxOSC:
			f.buf = append(f.buf, value)
			if value == 0x1b {
				f.state = filterTmuxEscape
			}
		case filterTmuxEscape:
			f.buf = append(f.buf, value)
			if value == '\\' {
				output = f.finishTmux(output)
			} else {
				f.state = filterTmuxOSC
			}
		}
		if len(f.buf) > maxEscapeBytes {
			output = f.flushTo(output)
		}
	}
	return output
}

// Flush returns an incomplete escape sequence unchanged at end of stream.
func (f *Filter) Flush() []byte {
	return f.flushTo(nil)
}

func (f *Filter) finishOSC(output []byte) []byte {
	body := stripTerminator(f.buf[2:])
	if !f.consume(body) {
		output = append(output, f.buf...)
	}
	f.reset()
	return output
}

func (f *Filter) finishTmux(output []byte) []byte {
	prefixSize := 2 + len(tmuxPrefix)
	body := stripTerminator(f.buf[prefixSize : len(f.buf)-2])
	if !f.consume(body) {
		output = append(output, f.buf...)
	}
	f.reset()
	return output
}

func (f *Filter) consume(body []byte) bool {
	if len(body) < 4 || string(body[:3]) != "52;" {
		return false
	}
	separator := -1
	for index, value := range body[3:] {
		if value == ';' {
			separator = index + 3
			break
		}
	}
	if separator < 0 {
		return false
	}
	encoded := body[separator+1:]
	if len(encoded) > maxEscapeBytes {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(string(encoded))
	}
	if err != nil || len(decoded) > maxClipboardSize {
		return false
	}
	f.sink(decoded)
	return true
}

func (f *Filter) flushTo(output []byte) []byte {
	output = append(output, f.buf...)
	f.reset()
	return output
}

func (f *Filter) reset() {
	f.buf = f.buf[:0]
	f.state = filterNormal
}

func stripTerminator(body []byte) []byte {
	if len(body) >= 2 && body[len(body)-2] == 0x1b && body[len(body)-1] == '\\' {
		return body[:len(body)-2]
	}
	if len(body) > 0 && body[len(body)-1] == 0x07 {
		return body[:len(body)-1]
	}
	return body
}
