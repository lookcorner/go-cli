package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	inspectapp "github.com/lookcorner/go-cli/internal/inspect"
)

func runInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	configPath := flags.String("config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("inspect does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := inspectapp.Build(cwd, *configPath)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printInspectReport(stdout, report)
	return nil
}

func printInspectReport(output io.Writer, report inspectapp.Report) {
	fmt.Fprintf(output, "Gork %s\n", cleanCLIText(report.GorkVersion))
	fmt.Fprintf(output, "Working directory: %s\n", cleanCLIText(report.CWD))
	if report.ProjectRoot != "" {
		fmt.Fprintf(output, "Project root: %s\n", cleanCLIText(report.ProjectRoot))
	}
	fmt.Fprintf(output, "Project trusted: %t\n", report.ProjectTrusted)
	fmt.Fprintf(output, "Permission mode: %s (%d rules)\n", cleanCLIText(report.Permissions.Mode), report.Permissions.Rules)
	fmt.Fprintf(output, "API-key authentication disabled: %t\n", report.LoginPolicy.APIKeyAuthDisabled)

	printInspectSection(output, "Project instructions", len(report.Instructions), func() {
		for _, item := range report.Instructions {
			fmt.Fprintf(output, "  %s (%d bytes, ~%d tokens)\n", cleanCLIText(item.Path), item.SizeBytes, item.ApproxTokens)
		}
	})
	printInspectSection(output, "Hooks", len(report.Hooks), func() {
		for _, item := range report.Hooks {
			fmt.Fprintf(output, "  %s: %s/%s%s\n", cleanCLIText(item.Name), cleanCLIText(item.Event), cleanCLIText(item.Type), inspectDisabled(item.Disabled))
		}
	})
	printInspectSection(output, "Skills", len(report.Skills), func() {
		for _, item := range report.Skills {
			fmt.Fprintf(output, "  %s [%s]%s\n", cleanCLIText(item.Name), cleanCLIText(item.Source), inspectDisabled(!item.Enabled))
		}
	})
	printInspectSection(output, "Agents", len(report.Agents), func() {
		for _, item := range report.Agents {
			fmt.Fprintf(output, "  %s [%s]%s\n", cleanCLIText(item.Name), cleanCLIText(item.Scope), inspectDisabled(!item.Enabled))
		}
	})
	printInspectSection(output, "Plugins", len(report.Plugins), func() {
		for _, item := range report.Plugins {
			fmt.Fprintf(output, "  %s [%s]%s\n", cleanCLIText(item.Name), cleanCLIText(item.Scope), inspectDisabled(!item.Enabled))
		}
	})
	printInspectSection(output, "Marketplaces", len(report.Marketplaces), func() {
		for _, item := range report.Marketplaces {
			fmt.Fprintf(output, "  %s [%s]: %s\n", cleanCLIText(item.Name), cleanCLIText(item.Kind), cleanCLIText(item.Path))
		}
	})
	printInspectSection(output, "MCP servers", len(report.MCPServers), func() {
		for _, item := range report.MCPServers {
			fmt.Fprintf(output, "  %s [%s]%s\n", cleanCLIText(item.Name), cleanCLIText(item.Transport), inspectDisabled(!item.Enabled))
		}
	})
	printInspectSection(output, "LSP servers", len(report.LSPServers), func() {
		for _, item := range report.LSPServers {
			fmt.Fprintf(output, "  %s: %s%s\n", cleanCLIText(item.Name), cleanCLIText(item.Command), inspectDisabled(!item.Enabled))
		}
	})
	printInspectSection(output, "Config sources", len(report.ConfigSources), func() {
		for _, item := range report.ConfigSources {
			fmt.Fprintf(output, "  %s [%s]\n", cleanCLIText(item.Path), cleanCLIText(item.Role))
		}
	})
	if len(report.DiscoveryWarnings) > 0 {
		printInspectSection(output, "Warnings", len(report.DiscoveryWarnings), func() {
			for _, warning := range report.DiscoveryWarnings {
				fmt.Fprintln(output, " ", cleanCLIText(warning))
			}
		})
	}
}

func printInspectSection(output io.Writer, title string, count int, printItems func()) {
	fmt.Fprintf(output, "\n%s (%d):\n", title, count)
	if count == 0 {
		fmt.Fprintln(output, "  (none)")
		return
	}
	printItems()
}

func inspectDisabled(disabled bool) string {
	if disabled {
		return " (disabled)"
	}
	return ""
}
