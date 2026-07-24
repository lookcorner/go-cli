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

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func runExport(args []string, stdout, stderr io.Writer) error {
	return runExportCommand("", args, stdout, stderr, copyToClipboard)
}

func runExportCommand(dir string, args []string, stdout, stderr io.Writer, copyText func(string) error) error {
	sessionID, output, clipboard, err := parseExportArgs(args)
	if err != nil {
		exportUsage(stderr)
		return err
	}
	path, err := sessionlog.PathForID(dir, sessionID)
	if err != nil {
		return err
	}
	content, err := sessionlog.ExportMarkdown(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("session %q not found", sessionID)
		}
		return err
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("session %q has no conversation content to export", sessionID)
	}
	if output != "" {
		target, err := exportPath(output)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create export directory: %w", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write conversation export: %w", err)
		}
		fmt.Fprintf(stderr, "Conversation exported to %s\n", target)
		return nil
	}
	if clipboard {
		if err := copyText(content); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Conversation copied to clipboard (%d chars, %d lines)\n", len(content), strings.Count(content, "\n")+1)
		return nil
	}
	fmt.Fprintln(stdout, content)
	return nil
}

func parseExportArgs(args []string) (sessionID, output string, clipboard bool, err error) {
	positional := make([]string, 0, 2)
	options := true
	for _, arg := range args {
		switch {
		case options && arg == "--":
			options = false
		case options && (arg == "-c" || arg == "--clipboard"):
			clipboard = true
		case options && strings.HasPrefix(arg, "-"):
			return "", "", false, fmt.Errorf("unknown export option %q", cleanCLIText(arg))
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		return "", "", false, errors.New("export requires a session ID and optional output path")
	}
	sessionID = positional[0]
	if len(positional) == 2 {
		output = positional[1]
	}
	return sessionID, output, clipboard, nil
}

func exportUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: gork export <session-id> [output] [-c|--clipboard]")
}

func exportPath(value string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if !filepath.IsAbs(value) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve export directory: %w", err)
		}
		value = filepath.Join(cwd, value)
	}
	return filepath.Clean(value), nil
}

func copyToClipboard(text string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "pbcopy"
	case "windows":
		name = "clip"
	default:
		for _, candidate := range []struct {
			name string
			args []string
		}{{"wl-copy", nil}, {"xclip", []string{"-selection", "clipboard"}}, {"xsel", []string{"--clipboard", "--input"}}} {
			if _, err := exec.LookPath(candidate.name); err == nil {
				name, args = candidate.name, candidate.args
				break
			}
		}
	}
	if name == "" {
		return errors.New("no native clipboard tool is available")
	}
	command := exec.Command(name, args...)
	command.Stdin = strings.NewReader(text)
	if output, err := command.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("copy conversation to clipboard: %w: %s", err, cleanCLIText(detail))
		}
		return fmt.Errorf("copy conversation to clipboard: %w", err)
	}
	return nil
}
