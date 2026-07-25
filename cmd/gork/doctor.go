package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lookcorner/go-cli/internal/terminaldiag"
)

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gork doctor [--json]")
		flags.PrintDefaults()
	}
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
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
