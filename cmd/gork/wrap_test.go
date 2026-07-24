package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestWrapPlansDirectAndShellRoutes(t *testing.T) {
	direct, fallback := wrapPlans([]string{os.Args[0], "a b", "don't"})
	want := wrapPlan{program: os.Args[0], args: []string{"a b", "don't"}}
	if !reflect.DeepEqual(direct, want) || !reflect.DeepEqual(fallback, want) {
		t.Fatalf("direct=%#v fallback=%#v", direct, fallback)
	}
	if runtime.GOOS == "windows" {
		return
	}
	wrapped, fallback := wrapPlans([]string{"printf '%s' shell"})
	if !reflect.DeepEqual(wrapped.args, []string{"-i", "-c", "printf '%s' shell"}) ||
		!reflect.DeepEqual(fallback.args, []string{"-c", "printf '%s' shell"}) {
		t.Fatalf("wrapped=%#v fallback=%#v", wrapped, fallback)
	}
}

func TestJoinWrapCommandQuotesTail(t *testing.T) {
	got := joinWrapCommand([]string{"alias", "a b", "don't", "$HOME", ""})
	want := "alias 'a b' 'don'\\''t' '$HOME' ''"
	if got != want {
		t.Fatalf("command=%q", got)
	}
}

func TestRunWrapDirectPreservesOutputAndExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	var stdout, stderr bytes.Buffer
	if err := runWrap([]string{"/bin/sh", "-c", "printf wrapped"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "wrapped" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	err := runWrap([]string{"/bin/sh", "-c", "exit 7"}, strings.NewReader(""), &stdout, &stderr)
	var exit commandExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit=%v", err)
	}
}

func TestRunWrapRejectsMissingCommand(t *testing.T) {
	var stderr bytes.Buffer
	if err := runWrap(nil, strings.NewReader(""), &bytes.Buffer{}, &stderr); err == nil ||
		!strings.Contains(stderr.String(), "Usage: gork wrap") {
		t.Fatalf("error=%v stderr=%q", err, stderr.String())
	}
}

func TestRunWrapHelpAndOptionTerminator(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWrap([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); err != nil ||
		!strings.Contains(stdout.String(), "Usage: gork wrap") || stderr.Len() != 0 {
		t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if runtime.GOOS == "windows" {
		return
	}
	stdout.Reset()
	if err := runWrap([]string{"--", "/bin/sh", "-c", "printf options"}, strings.NewReader(""), &stdout, &stderr); err != nil ||
		stdout.String() != "options" {
		t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestWrapDispatchesBeforeConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), "missing"))
	var stdout, stderr bytes.Buffer
	err := runOnce([]string{"wrap", "/bin/sh", "-c", "printf early"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil || stdout.String() != "early" || stderr.Len() != 0 {
		t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestResolveWrapShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell")
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resolveWrapShell(path); got != path {
		t.Fatalf("shell=%q", got)
	}
	if got := resolveWrapShell(filepath.Join(t.TempDir(), "missing")); got != "/bin/sh" {
		t.Fatalf("fallback=%q", got)
	}
}
