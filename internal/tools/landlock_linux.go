//go:build linux

package tools

import (
	"fmt"
	"io"

	ll "github.com/landlock-lsm/go-landlock/landlock"
)

func applyParentLandlockResolved(resolved ResolvedSandboxProfile, workspace string, warn io.Writer) (bool, error) {
	paths := ParentLandlockPathsFromResolved(resolved, workspace)
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
			fmt.Fprintf(warn, "gork: parent Landlock skipped: no usable paths for profile %q\n", resolved.Name)
		}
		return false, nil
	}
	cfg := ll.V5
	if !resolved.Custom {
		// Built-ins: BestEffort succeeds even when Landlock is unavailable.
		cfg = cfg.BestEffort()
	}
	// RestrictPaths only — do not RestrictNet (LLM/MCP HTTP must keep working).
	if err := cfg.RestrictPaths(rules...); err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "gork: parent Landlock unavailable: %v\n", err)
		}
		return false, nil
	}
	return true, nil
}
