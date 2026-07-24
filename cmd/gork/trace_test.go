package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sessiontrace "github.com/lookcorner/go-cli/internal/trace"
)

func TestTraceCLIExportsWithReferenceOutputShapes(t *testing.T) {
	previous := exportTrace
	exportTrace = func(service sessiontrace.Service, sessionID, output string) (sessiontrace.Result, error) {
		if service.SessionDir != "sessions" || sessionID != "trace-one" || output != "out.tar.gz" {
			t.Fatalf("service=%#v session=%q output=%q", service, sessionID, output)
		}
		return sessiontrace.Result{SessionID: sessionID, Path: "/tmp/out.tar.gz", Size: 2048}, nil
	}
	t.Cleanup(func() { exportTrace = previous })

	var stdout, stderr bytes.Buffer
	err := runTrace([]string{"trace-one", "--output", "out.tar.gz", "--session-dir", "sessions"}, &stdout, &stderr)
	if err != nil || stdout.String() != "/tmp/out.tar.gz\n" ||
		!strings.Contains(stderr.String(), "Trace uploads are disabled") ||
		!strings.Contains(stderr.String(), "Session trace exported (2 KB)") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := runTrace([]string{"--json", "--local", "trace-one", "-o", "out.tar.gz", "--session-dir", "sessions"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil ||
		payload["session_id"] != "trace-one" || payload["status"] != "exported" ||
		payload["local_path"] != "/tmp/out.tar.gz" || stderr.Len() != 0 {
		t.Fatalf("payload=%#v err=%v stderr=%q", payload, err, stderr.String())
	}
}

func TestTraceCLIHelpAndArgumentErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runTrace([]string{"--help"}, &stdout, &stderr); err != nil ||
		stdout.String() != traceUsage+"\n" || stderr.Len() != 0 {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	for _, args := range [][]string{nil, {"one", "two"}, {"one", "--unknown"}, {"one", "--output"}} {
		if err := runTrace(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
}

func TestTraceCLIPropagatesExportError(t *testing.T) {
	previous := exportTrace
	want := errors.New("export failed")
	exportTrace = func(sessiontrace.Service, string, string) (sessiontrace.Result, error) {
		return sessiontrace.Result{}, want
	}
	t.Cleanup(func() { exportTrace = previous })
	if err := runTrace([]string{"session"}, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}
