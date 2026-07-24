package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/update"
	"github.com/lookcorner/go-cli/internal/version"
	"golang.org/x/mod/semver"
)

func runUpdate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gork update [--check] [--json] [--force-reinstall] [--version VERSION] [--alpha|--stable]")
		fmt.Fprintln(stderr, "  --alpha             switch to the alpha release channel")
		fmt.Fprintln(stderr, "  --check             check update status without installing")
		fmt.Fprintln(stderr, "  --force-reinstall   force reinstall even when current")
		fmt.Fprintln(stderr, "  --json              emit machine-readable status with --check")
		fmt.Fprintln(stderr, "  --stable            switch to the stable release channel")
		fmt.Fprintln(stderr, "  --version VERSION   install a specific semantic version")
	}
	check := flags.Bool("check", false, "check update status without installing")
	asJSON := flags.Bool("json", false, "emit machine-readable status with --check")
	flags.Bool("force-reinstall", false, "force reinstall even when current")
	target := flags.String("version", "", "install a specific semantic version")
	alpha := flags.Bool("alpha", false, "switch to the alpha release channel")
	stable := flags.Bool("stable", false, "switch to the stable release channel")
	enterprise := flags.Bool("enterprise", false, "switch to the enterprise release channel")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("update does not accept positional arguments")
	}
	if *asJSON && !*check {
		return errors.New("--json requires --check")
	}
	if *check && *target != "" {
		return errors.New("--version cannot be used with --check")
	}
	channel, err := selectedUpdateChannel(*alpha, *stable, *enterprise)
	if err != nil {
		return err
	}
	if *check {
		if channel != "" {
			current, readErr := config.ReleaseChannel("")
			if readErr != nil {
				return readErr
			}
			if channel != current {
				if err := config.UpdateReleaseChannel("", channel); err != nil {
					return err
				}
				fmt.Fprintf(stderr, "Switched to %s channel.\n", channel)
			}
		} else {
			channel, err = config.ReleaseChannel("")
			if err != nil {
				return err
			}
		}
		status := update.Check(version.Current, channel)
		if *asJSON {
			return json.NewEncoder(stdout).Encode(status)
		}
		fmt.Fprintf(stdout, "Gork Build - v%s [%s]\n", cleanCLIText(status.CurrentVersion), status.Channel)
		fmt.Fprintln(stdout, "Auto-update: disabled (privacy build never installs from vendor channels).")
		fmt.Fprintln(stdout, update.BlockedMessage())
		return nil
	}
	if *target != "" && !semver.IsValid("v"+*target) {
		return fmt.Errorf("%q is not a valid version; expected semver like 0.1.150", cleanCLIText(*target))
	}
	return errors.New(update.BlockedMessage())
}

func selectedUpdateChannel(alpha, stable, enterprise bool) (string, error) {
	count := 0
	for _, selected := range []bool{alpha, stable, enterprise} {
		if selected {
			count++
		}
	}
	if count > 1 {
		return "", errors.New("--alpha, --stable, and --enterprise are mutually exclusive")
	}
	switch {
	case alpha:
		return "alpha", nil
	case stable:
		return "stable", nil
	case enterprise:
		return "enterprise", nil
	default:
		return "", nil
	}
}
