package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lookcorner/go-cli/internal/terminaldiag"
)

func runDoctor(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "fix" {
		return runDoctorFix(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gork doctor [--json]")
		fmt.Fprintln(stderr, "       gork doctor fix [ssh-wrap|tmux-clipboard|dcs-passthrough|tmux-extended-keys|tmux-truecolor|colorterm] [--yes]")
		flags.PrintDefaults()
	}
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments; use `gork doctor fix`")
	}
	if *asJSON {
		payload, err := terminaldiag.ReportJSON()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", payload)
		return err
	}
	_, err := fmt.Fprintln(stdout, terminaldiag.Report())
	return err
}

func runDoctorFix(args []string, stdout, stderr io.Writer) error {
	yes, rest, err := parseDoctorFixArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stderr, "Usage: gork doctor fix [ssh-wrap|tmux-clipboard|dcs-passthrough|tmux-extended-keys|tmux-truecolor|colorterm] [--yes]")
		return nil
	} else if err != nil {
		return err
	}
	env := terminaldiag.DefaultFixEnv()
	if len(rest) == 0 {
		if yes {
			return errors.New("--yes requires a fix id")
		}
		_, err := fmt.Fprintln(stdout, terminaldiag.FormatFixListing(terminaldiag.ListAutomaticFixes(env)))
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: gork doctor fix [ssh-wrap|tmux-clipboard|dcs-passthrough|tmux-extended-keys|tmux-truecolor|colorterm] [--yes]")
	}
	id, err := terminaldiag.ResolveFixID(rest[0])
	if err != nil {
		return err
	}
	plan, err := terminaldiag.PlanFix(env, id)
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintln(stdout, terminaldiag.FormatFixPreview(plan))
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Re-run with --yes to apply.")
		return nil
	}
	outcome, err := terminaldiag.ApplyFix(plan)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, terminaldiag.FormatFixSuccess(outcome))
	return err
}

func parseDoctorFixArgs(args []string) (yes bool, rest []string, err error) {
	for _, arg := range args {
		switch {
		case arg == "--yes":
			yes = true
		case arg == "-h" || arg == "--help":
			return false, nil, flag.ErrHelp
		case strings.HasPrefix(arg, "-"):
			return false, nil, fmt.Errorf("unknown flag %s", arg)
		default:
			rest = append(rest, arg)
		}
	}
	return yes, rest, nil
}
