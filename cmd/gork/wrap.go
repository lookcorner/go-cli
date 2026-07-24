package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	sessionwrap "github.com/lookcorner/go-cli/internal/wrap"
)

type wrapPlan struct {
	program string
	args    []string
}

type commandExitError struct{ code int }

func (e commandExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.code) }
func (e commandExitError) ExitCode() int { return e.code }

func runWrap(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		wrapUsage(stdout)
		return nil
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		wrapUsage(stderr)
		return errors.New("wrap requires a command")
	}
	wrapped, fallback := wrapPlans(args)
	input, inputOK := stdin.(*os.File)
	output, outputOK := stdout.(*os.File)
	errorsFile, errorsOK := stderr.(*os.File)
	if inputOK && outputOK && errorsOK && sessionwrap.PTYSupported() &&
		terminalIO(input) && terminalIO(output) && terminalIO(errorsFile) {
		code, err := sessionwrap.RunPTY(wrapped.program, wrapped.args, input, output, errorsFile, copyToClipboard)
		if err == nil {
			if code != 0 {
				return commandExitError{code: code}
			}
			return nil
		}
		fmt.Fprintln(stderr, "gork wrap: wrapped mode failed, running without PTY wrapping:", err)
	}
	return runWrappedDirect(fallback, stdin, stdout, stderr)
}

func wrapPlans(command []string) (wrapPlan, wrapPlan) {
	direct := wrapPlan{program: command[0], args: append([]string(nil), command[1:]...)}
	if runtime.GOOS == "windows" {
		return direct, direct
	}
	shell := resolveWrapShell(os.Getenv("SHELL"))
	_, pathErr := exec.LookPath(command[0])
	if len(command) == 1 && strings.IndexFunc(command[0], unicode.IsSpace) >= 0 {
		return shellWrapPlan(shell, command[0], true), shellWrapPlan(shell, command[0], false)
	}
	if command[0] != "" && !strings.ContainsRune(command[0], filepath.Separator) &&
		strings.IndexFunc(command[0], unicode.IsSpace) < 0 && pathErr != nil {
		line := joinWrapCommand(command)
		return shellWrapPlan(shell, line, true), shellWrapPlan(shell, line, false)
	}
	return direct, direct
}

func shellWrapPlan(shell, line string, interactive bool) wrapPlan {
	args := []string{"-c", line}
	if interactive {
		args = []string{"-i", "-c", line}
	}
	return wrapPlan{program: shell, args: args}
}

func resolveWrapShell(value string) string {
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		return value
	}
	return "/bin/sh"
}

func joinWrapCommand(command []string) string {
	line := command[0]
	for _, word := range command[1:] {
		line += " '" + strings.ReplaceAll(word, "'", "'\\''") + "'"
	}
	return line
}

func runWrappedDirect(plan wrapPlan, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(plan.program, plan.args...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	err := command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return commandExitError{code: exit.ExitCode()}
	}
	if err != nil {
		return fmt.Errorf("failed to run %s: %w", cleanCLIText(plan.program), err)
	}
	return nil
}

func wrapUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork wrap <command> [args...]")
}
