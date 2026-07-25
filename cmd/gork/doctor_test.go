package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorCLIHumanAndJSON(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM_PROGRAM", "TestTerm")
	t.Setenv("TMUX", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("NO_COLOR", "")

	var human, stderr bytes.Buffer
	if err := runDoctor(nil, &human, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Environment", "terminal     TestTerm", "Clipboard routes"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human missing %q:\n%s", want, human.String())
		}
	}

	var jsonOutput bytes.Buffer
	if err := runDoctor([]string{"--json"}, &jsonOutput, &stderr); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["schemaVersion"] != "1" {
		t.Fatalf("schema=%#v", report["schemaVersion"])
	}
	facts, _ := report["facts"].(map[string]any)
	if facts["terminal"] != "TestTerm" {
		t.Fatalf("facts=%#v", facts)
	}
	counts, _ := report["counts"].(map[string]any)
	if _, ok := counts["issues"]; !ok {
		t.Fatalf("counts=%#v", counts)
	}
}

func TestDoctorCLIRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runDoctor([]string{"extra"}, &stdout, &stderr); err == nil {
		t.Fatal("doctor accepted a positional argument")
	}
}

func TestDoctorEarlyDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runOnce([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"schemaVersion"`) {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}
