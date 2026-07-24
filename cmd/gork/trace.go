package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	sessiontrace "github.com/lookcorner/go-cli/internal/trace"
)

const traceUsage = "Usage: gork trace <session-id> [--local] [-o|--output path] [--json] [--session-dir path]"

var exportTrace = func(service sessiontrace.Service, sessionID, output string) (sessiontrace.Result, error) {
	return service.Export(sessionID, output)
}

func runTrace(args []string, stdout, stderr io.Writer) error {
	sessionID, output, sessionDir, local, asJSON, help, err := parseTraceArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, traceUsage)
		return err
	}
	if help {
		fmt.Fprintln(stdout, traceUsage)
		return nil
	}
	if !local && !asJSON {
		fmt.Fprintln(stderr, "Trace uploads are disabled in this privacy build.")
		fmt.Fprintln(stderr, "Falling back to local export.")
	}
	result, err := exportTrace(sessiontrace.Service{SessionDir: sessionDir}, sessionID, output)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"session_id": result.SessionID,
			"status":     "exported",
			"local_path": result.Path,
		})
	}
	fmt.Fprintf(stderr, "Session trace exported (%d KB):\n  %s\n", result.Size/1024, result.Path)
	fmt.Fprintln(stdout, result.Path)
	return nil
}

func parseTraceArgs(args []string) (sessionID, output, sessionDir string, local, asJSON, help bool, err error) {
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		next := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		switch arg {
		case "--local":
			local = true
		case "--json":
			asJSON = true
		case "-o", "--output":
			output, err = next()
		case "--session-dir":
			sessionDir, err = next()
		case "-h", "--help":
			return "", "", "", false, false, true, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", "", false, false, false, fmt.Errorf("unknown trace option %q", cleanCLIText(arg))
			}
			positionals = append(positionals, arg)
		}
		if err != nil {
			return "", "", "", false, false, false, err
		}
	}
	if len(positionals) != 1 {
		return "", "", "", false, false, false, errors.New("trace requires one session ID")
	}
	return positionals[0], output, sessionDir, local, asJSON, false, nil
}
