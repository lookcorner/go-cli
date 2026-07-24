package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/leader"
)

func TestLeaderListEmpty(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"leader", "list"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "No leader candidates found.\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWriteLeaderListJSONMatchesReferenceFields(t *testing.T) {
	pid := uint32(42)
	var output bytes.Buffer
	if err := writeLeaderList([]leader.Descriptor{{
		PIDFromLock: &pid, LockPath: "/tmp/leader.lock", SocketPath: "/tmp/leader.sock",
		State: leader.StateReachable, LiveInfo: &leader.Info{PID: 43},
	}}, true, &output); err != nil {
		t.Fatal(err)
	}
	var values []map[string]any
	if err := json.Unmarshal(output.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0]["pid"] != float64(43) || values[0]["pidFromLock"] != float64(42) ||
		values[0]["pidLive"] != float64(43) || values[0]["classification"] != "Reachable" {
		t.Fatalf("values=%#v", values)
	}
}

func TestLeaderRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"list", "extra"},
		{"info", "--pid", "4294967296"},
		{"kill", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runLeader(args, &stdout, &stderr); err == nil {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

func TestLeaderKillEmpty(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"leader", "kill"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.String() != "No leader candidates found.\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
