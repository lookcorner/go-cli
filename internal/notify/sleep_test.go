package notify

import (
	"errors"
	"testing"
)

func TestSleepInhibitorIsIdempotent(t *testing.T) {
	starts, stops := 0, 0
	inhibitor := &SleepInhibitor{enabled: true, start: func() (func(), error) {
		starts++
		return func() { stops++ }, nil
	}}

	inhibitor.Inhibit()
	inhibitor.Inhibit()
	if starts != 1 || !inhibitor.Active() {
		t.Fatalf("starts=%d active=%v", starts, inhibitor.Active())
	}
	inhibitor.Release()
	inhibitor.Release()
	if stops != 1 || inhibitor.Active() {
		t.Fatalf("stops=%d active=%v", stops, inhibitor.Active())
	}
	inhibitor.Inhibit()
	if starts != 2 || !inhibitor.Active() {
		t.Fatalf("re-inhibit starts=%d active=%v", starts, inhibitor.Active())
	}
}

func TestSleepInhibitorStopsProbingAfterFailure(t *testing.T) {
	starts := 0
	inhibitor := &SleepInhibitor{enabled: true, start: func() (func(), error) {
		starts++
		return nil, errors.New("no systemd-inhibit")
	}}
	inhibitor.Inhibit()
	inhibitor.Inhibit()
	if starts != 1 || inhibitor.Active() {
		t.Fatalf("starts=%d active=%v", starts, inhibitor.Active())
	}
	inhibitor.Release()
}

func TestSleepInhibitorRespectsDisabledAndNilReceiver(t *testing.T) {
	starts := 0
	disabled := NewSleepInhibitor(false)
	disabled.start = func() (func(), error) { starts++; return func() {}, nil }
	disabled.Inhibit()
	disabled.Release()
	if starts != 0 || disabled.Active() {
		t.Fatalf("disabled starts=%d active=%v", starts, disabled.Active())
	}

	var absent *SleepInhibitor
	absent.Inhibit()
	absent.Release()
	if absent.Active() {
		t.Fatal("nil inhibitor reported active")
	}
}
