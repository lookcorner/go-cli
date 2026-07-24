package wrap

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestFilterConsumesOSC52AcrossChunks(t *testing.T) {
	var clips [][]byte
	filter := NewFilter(func(data []byte) { clips = append(clips, append([]byte(nil), data...)) })
	sequence := []byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("copied")) + "\x1b\\")
	input := append(append([]byte("before "), sequence...), []byte(" after")...)
	var output []byte
	for _, value := range input {
		output = append(output, filter.Feed([]byte{value})...)
	}
	output = append(output, filter.Flush()...)
	if string(output) != "before  after" || len(clips) != 1 || string(clips[0]) != "copied" {
		t.Fatalf("output=%q clips=%q", output, clips)
	}
}

func TestFilterConsumesBELAndUnpaddedPayload(t *testing.T) {
	var copied []byte
	filter := NewFilter(func(data []byte) { copied = append([]byte(nil), data...) })
	encoded := base64.RawStdEncoding.EncodeToString([]byte("hello"))
	if output := filter.Feed([]byte("\x1b]52;s0;" + encoded + "\x07")); len(output) != 0 || string(copied) != "hello" {
		t.Fatalf("output=%q copied=%q", output, copied)
	}
}

func TestFilterConsumesTmuxWrappedOSC52(t *testing.T) {
	var copied []byte
	filter := NewFilter(func(data []byte) { copied = append([]byte(nil), data...) })
	encoded := base64.StdEncoding.EncodeToString([]byte("tmux"))
	sequence := []byte("\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\x07\x1b\\")
	if output := filter.Feed(sequence); len(output) != 0 || string(copied) != "tmux" {
		t.Fatalf("output=%q copied=%q", output, copied)
	}
}

func TestFilterPreservesOtherAndInvalidSequences(t *testing.T) {
	inputs := [][]byte{
		[]byte("\x1b[31mred\x1b[0m"),
		[]byte("\x1b]0;title\x07"),
		[]byte("\x1b]52;c;!!!\x07"),
		[]byte("\x1bPnot-tmux\x1b\\"),
	}
	for _, input := range inputs {
		filter := NewFilter(nil)
		output := append(filter.Feed(input), filter.Flush()...)
		if !bytes.Equal(output, input) {
			t.Fatalf("input=%q output=%q", input, output)
		}
	}
}

func TestFilterFlushesIncompleteAndBoundsBuffer(t *testing.T) {
	filter := NewFilter(nil)
	incomplete := []byte("\x1b]52;c;abc")
	if output := append(filter.Feed(incomplete), filter.Flush()...); !bytes.Equal(output, incomplete) {
		t.Fatalf("output=%q", output)
	}
	oversized := append([]byte("\x1b]52;c;"), bytes.Repeat([]byte("a"), maxEscapeBytes+1)...)
	filter = NewFilter(nil)
	output := append(filter.Feed(oversized), filter.Flush()...)
	if !bytes.Equal(output, oversized) {
		t.Fatalf("oversized sequence changed: got=%d want=%d", len(output), len(oversized))
	}
}
