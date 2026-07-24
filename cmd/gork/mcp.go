package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lookcorner/go-cli/internal/mcpadmin"
)

var probeMCPServer = mcpadmin.Probe

func runMCP(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("MCP command is required: list, add, remove, or doctor")
	}
	if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
		printMCPHelp(stdout, args[0])
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return runMCPList(cwd, args[1:], stdout, stderr)
	case "add":
		return runMCPAdd(cwd, args[1:], stdout)
	case "remove":
		return runMCPRemove(cwd, args[1:], stdout, stderr)
	case "doctor":
		return runMCPDoctor(cwd, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown MCP command %q", cleanCLIText(args[0]))
	}
}

func printMCPHelp(output io.Writer, command string) {
	usage := map[string]string{
		"list":   "Usage: gork mcp list [--json] [--config path]",
		"add":    "Usage: gork mcp add [--transport stdio|http|sse] [--scope user|project] [-e KEY=value] [-H \"Name: value\"] <name> -- <command> [args]",
		"remove": "Usage: gork mcp remove <name> [--scope user|project] [--config path]",
		"doctor": "Usage: gork mcp doctor [name] [--json] [--config path]",
	}
	if text, ok := usage[command]; ok {
		fmt.Fprintln(output, text)
	}
}

func runMCPList(cwd string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gork mcp list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	configPath := flags.String("config", "", "path to user config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gork mcp list [--json] [--config path]")
	}
	entries, err := mcpadmin.List(cwd, *configPath)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No MCP servers configured. Run `gork mcp add --help` to get started.")
		return nil
	}
	for _, entry := range entries {
		target := entry.Config.Command
		if entry.Config.URL != "" {
			target = entry.Config.URL
		} else if len(entry.Config.Args) > 0 {
			target += " " + strings.Join(entry.Config.Args, " ")
		}
		status := ""
		if !entry.Config.IsEnabled() {
			status = " (disabled)"
		}
		fmt.Fprintf(stdout, "  %s: %s%s (%s)\n", cleanCLIText(entry.Name), cleanCLIText(target), status, entry.Scope)
	}
	return nil
}

func runMCPAdd(cwd string, args []string, stdout io.Writer) error {
	parsed, err := parseMCPAdd(args)
	if err != nil {
		return err
	}
	path, err := mcpadmin.Add(cwd, parsed.configPath, parsed.request)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s MCP server %q to %s config\n", parsed.request.Transport, cleanCLIText(parsed.request.Name), parsed.request.Scope)
	fmt.Fprintf(stdout, "File modified: %s\n", cleanCLIText(path))
	return nil
}

type mcpAddOptions struct {
	configPath string
	request    mcpadmin.AddRequest
}

func parseMCPAdd(args []string) (mcpAddOptions, error) {
	result := mcpAddOptions{request: mcpadmin.AddRequest{Scope: mcpadmin.UserScope, Transport: "stdio"}}
	var positionals, commandArgs []string
	afterSeparator := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if afterSeparator {
			commandArgs = append(commandArgs, arg)
			continue
		}
		if arg == "--" {
			afterSeparator = true
			continue
		}
		next := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		switch arg {
		case "-t", "--transport":
			value, err := next()
			if err != nil {
				return result, err
			}
			result.request.Transport = strings.ToLower(value)
		case "-s", "--scope":
			value, err := next()
			if err != nil {
				return result, err
			}
			result.request.Scope = mcpadmin.Scope(strings.ToLower(value))
		case "--config":
			value, err := next()
			if err != nil {
				return result, err
			}
			result.configPath = value
		case "-e", "--env":
			value, err := next()
			if err != nil {
				return result, err
			}
			name, content, ok := strings.Cut(value, "=")
			if !ok || name == "" {
				return result, fmt.Errorf("invalid environment variable %q; expected KEY=value", value)
			}
			if result.request.Env == nil {
				result.request.Env = make(map[string]string)
			}
			result.request.Env[name] = content
		case "-H", "--header":
			value, err := next()
			if err != nil {
				return result, err
			}
			name, content, ok := strings.Cut(value, ":")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				return result, fmt.Errorf("invalid header %q; expected Name: value", value)
			}
			if result.request.Headers == nil {
				result.request.Headers = make(map[string]string)
			}
			result.request.Headers[name] = strings.TrimSpace(content)
		default:
			if strings.HasPrefix(arg, "-") {
				return result, fmt.Errorf("unknown MCP add option %q", cleanCLIText(arg))
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) == 0 {
		return result, errors.New("usage: gork mcp add [options] <name> -- <command> [args]")
	}
	result.request.Name = positionals[0]
	sources := append([]string(nil), positionals[1:]...)
	sources = append(sources, commandArgs...)
	if len(sources) == 0 {
		return result, errors.New("MCP server command or URL is required")
	}
	result.request.Source = sources[0]
	result.request.Args = sources[1:]
	return result, nil
}

func runMCPRemove(cwd string, args []string, stdout, stderr io.Writer) error {
	name, scope, configPath, err := parseMCPRemove(args)
	if err != nil {
		return err
	}
	if name == "" {
		return errors.New("usage: gork mcp remove <name> [--scope user|project]")
	}
	removedScope, path, err := mcpadmin.Remove(cwd, configPath, name, mcpadmin.Scope(strings.ToLower(scope)))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed MCP server %q from %s config\n", cleanCLIText(name), removedScope)
	fmt.Fprintf(stdout, "File modified: %s\n", cleanCLIText(path))
	if remainingScope, remainingPath, found, lookupErr := mcpadmin.RemainingDefinition(cwd, configPath, name); lookupErr == nil && found {
		fmt.Fprintf(stderr, "note: %q is still defined in %s config: %s\n", cleanCLIText(name), remainingScope, cleanCLIText(remainingPath))
	}
	return nil
}

func parseMCPRemove(args []string) (name, scope, configPath string, err error) {
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
		case "-s", "--scope":
			scope, err = next()
		case "--config":
			configPath, err = next()
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", "", fmt.Errorf("unknown MCP remove option %q", cleanCLIText(arg))
			}
			positionals = append(positionals, arg)
		}
		if err != nil {
			return "", "", "", err
		}
	}
	if len(positionals) != 1 {
		return "", "", "", errors.New("usage: gork mcp remove <name> [--scope user|project]")
	}
	return positionals[0], scope, configPath, nil
}

func runMCPDoctor(cwd string, args []string, stdout, stderr io.Writer) error {
	name, asJSON, configPath, err := parseMCPDoctor(args)
	if err != nil {
		return err
	}
	report, err := mcpadmin.Doctor(context.Background(), cwd, configPath, name, probeMCPServer)
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else if len(report.Servers) == 0 {
		fmt.Fprintln(stdout, "No MCP servers configured.")
	} else {
		for _, server := range report.Servers {
			fmt.Fprintf(stdout, "  %s (%s: %s)\n", cleanCLIText(server.Name), server.Transport, cleanCLIText(server.Target))
			for _, check := range server.Checks {
				marker := "x"
				if check.Passed {
					marker = "ok"
				}
				fmt.Fprintf(stdout, "    %s %s", marker, cleanCLIText(check.Label))
				if check.Detail != "" {
					fmt.Fprintf(stdout, " (%s)", cleanCLIText(check.Detail))
				}
				fmt.Fprintln(stdout)
				if check.Hint != "" {
					fmt.Fprintf(stdout, "    -> %s\n", cleanCLIText(check.Hint))
				}
			}
		}
		fmt.Fprintf(stdout, "Found %d healthy, %d failing.\n", report.HealthyCount, report.FailingCount)
	}
	if report.FailingCount > 0 {
		return fmt.Errorf("%d MCP server(s) failed diagnostics", report.FailingCount)
	}
	return nil
}

func parseMCPDoctor(args []string) (name string, asJSON bool, configPath string, err error) {
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--json":
			asJSON = true
		case "--config":
			index++
			if index >= len(args) {
				return "", false, "", errors.New("--config requires a value")
			}
			configPath = args[index]
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, "", fmt.Errorf("unknown MCP doctor option %q", cleanCLIText(arg))
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", false, "", errors.New("usage: gork mcp doctor [--json] [name]")
	}
	if len(positionals) == 1 {
		name = positionals[0]
	}
	return name, asJSON, configPath, nil
}
