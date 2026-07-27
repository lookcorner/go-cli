//go:build linux

package tools

import (
	"fmt"
	"io"

	ll "github.com/landlock-lsm/go-landlock/landlock"
)

func applyParentLandlock(profile SandboxProfile, workspace string, warn io.Writer) error {
	paths := ParentLandlockPathsFor(profile, workspace)
	rules := make([]ll.Rule, 0, 3)
	if len(paths.RODirs) > 0 {
		rules = append(rules, ll.RODirs(paths.RODirs...))
	}
	if len(paths.RWDirs) > 0 {
		rules = append(rules, ll.RWDirs(paths.RWDirs...))
	}
	if len(paths.ROFiles) > 0 {
		rules = append(rules, ll.ROFiles(paths.ROFiles...))
	}
	if len(rules) == 0 {
		if warn != nil {
			fmt.Fprintf(warn, "gork: parent Landlock skipped: no usable paths for profile %q\n", profile)
		}
		return nil
	}
	// BestEffort: succeed without confinement when Landlock ABI is unavailable.
	// RestrictPaths only — do not RestrictNet (LLM/MCP HTTP must keep working).
	if err := ll.V5.BestEffort().RestrictPaths(rules...); err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "gork: parent Landlock unavailable: %v\n", err)
		}
		return nil
	}
	return nil
}
