package wrap

import (
	"bytes"
	"testing"
)

func restoreFor(seqs ...[]byte) []byte {
	tracker := NewModeTracker()
	for _, seq := range seqs {
		tracker.ObserveCSI(seq)
	}
	return RestoreBytes(tracker.Snapshot())
}

func TestRestoreNothingLatched(t *testing.T) {
	if out := restoreFor(); len(out) != 0 {
		t.Fatalf("got %q", out)
	}
}

func TestRestoreBalancedModes(t *testing.T) {
	out := restoreFor(
		[]byte("\x1b[?1049h"), []byte("\x1b[?1003h"), []byte("\x1b[?2004h"),
		[]byte("\x1b[?25l"), []byte("\x1b[?25h"),
		[]byte("\x1b[?2004l"), []byte("\x1b[?1003l"), []byte("\x1b[?1049l"),
	)
	if len(out) != 0 {
		t.Fatalf("got %q", out)
	}
}

func TestRestoreLatchedMouseAndPaste(t *testing.T) {
	out := restoreFor([]byte("\x1b[?1003h"), []byte("\x1b[?2004h"))
	if !bytes.Equal(out, []byte("\x1b[?1003l\x1b[?2004l")) {
		t.Fatalf("got %q", out)
	}
}

func TestRestoreMultiParamSet(t *testing.T) {
	out := restoreFor([]byte("\x1b[?1002;1006h"))
	if !bytes.Equal(out, []byte("\x1b[?1002l\x1b[?1006l")) {
		t.Fatalf("got %q", out)
	}
}

func TestRestoreCursorHideInverted(t *testing.T) {
	if !bytes.Equal(restoreFor([]byte("\x1b[?25l")), []byte("\x1b[?25h")) {
		t.Fatal("hide should restore show")
	}
	if len(restoreFor([]byte("\x1b[?25l"), []byte("\x1b[?25h"))) != 0 {
		t.Fatal("balanced cursor must be empty")
	}
	if len(restoreFor([]byte("\x1b[?25h"))) != 0 {
		t.Fatal("bare show must not latch")
	}
}

func TestRestoreKittyDepth(t *testing.T) {
	out := restoreFor([]byte("\x1b[>1u"), []byte("\x1b[>11u"))
	if !bytes.Equal(out, []byte("\x1b[<u\x1b[<u")) {
		t.Fatalf("got %q", out)
	}
	if len(restoreFor([]byte("\x1b[>1u"), []byte("\x1b[>1u"), []byte("\x1b[<2u"))) != 0 {
		t.Fatal("CSI <2u should clear depth")
	}
	if len(restoreFor([]byte("\x1b[<u"))) != 0 {
		t.Fatal("pop must floor at zero")
	}
	if len(restoreFor([]byte("\x1b[>1u"), []byte("\x1b[<0u"))) != 0 {
		t.Fatal("CSI <0u means pop one")
	}
}

func TestRestoreOrder(t *testing.T) {
	out := restoreFor(
		[]byte("\x1b[?2026h"), []byte("\x1b[?1049h"), []byte("\x1b[?1003h"),
		[]byte("\x1b[?1006h"), []byte("\x1b[?2004h"), []byte("\x1b[?25l"),
		[]byte("\x1b[>1u"),
	)
	if !bytes.HasPrefix(out, []byte("\x1b[?2026l")) {
		t.Fatalf("sync must be first: %q", out)
	}
	if !bytes.HasSuffix(out, []byte("\x1b[?1049l")) {
		t.Fatalf("alt-screen must be last: %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[<u")) || bytes.Index(out, []byte("\x1b[<u")) > bytes.Index(out, []byte("\x1b[?1049l")) {
		t.Fatalf("kitty pop before alt leave: %q", out)
	}
}

func TestRestoreClaimOnce(t *testing.T) {
	tracker := NewModeTracker()
	tracker.ObserveCSI([]byte("\x1b[?1000h"))
	filter := &Filter{modes: tracker}
	first := filter.EmitRestore()
	second := filter.EmitRestore()
	if !bytes.Equal(first, []byte("\x1b[?1000l")) || len(second) != 0 {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestFilterObservesCSIAndForwards(t *testing.T) {
	filter := NewFilter(nil)
	input := []byte("hi\x1b[?1003h\x1b[?2004hbye")
	output := append(filter.Feed(input), filter.Flush()...)
	if !bytes.Equal(output, input) {
		t.Fatalf("output=%q", output)
	}
	got := RestoreBytes(filter.Modes().Snapshot())
	if !bytes.Equal(got, []byte("\x1b[?1003l\x1b[?2004l")) {
		t.Fatalf("restore=%q", got)
	}
}

func TestFilterDropsIncompleteCSIOnFlush(t *testing.T) {
	filter := NewFilter(nil)
	incomplete := []byte("\x1b[?1003")
	output := append(filter.Feed(incomplete), filter.Flush()...)
	if len(output) != 0 {
		t.Fatalf("incomplete CSI should not flush: %q", output)
	}
}
