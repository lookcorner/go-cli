package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lookcorner/go-cli/internal/version"
)

func runVersion(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gork version [--json]")
		flags.PrintDefaults()
	}
	asJSON := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("version does not accept positional arguments")
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]string{
			"currentVersion": version.Current,
			"channel":        version.Channel(),
		})
	}
	label := ""
	if channel := version.Channel(); channel != "unknown" {
		label = " [" + channel + "]"
	}
	fmt.Fprintf(stdout, "gork %s%s\n", cleanCLIText(version.Current), label)
	return nil
}
