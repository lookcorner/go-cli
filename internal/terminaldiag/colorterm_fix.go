package terminaldiag

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ColortermID     = "terminal.colorterm"
	ColortermHandle = "colorterm"
)

func (s ShellKind) ColortermExport() string {
	switch s {
	case ShellFish:
		return "set -gx COLORTERM truecolor"
	default:
		return "export COLORTERM=truecolor"
	}
}

func PlanColorterm(env FixEnv) (FixPlan, error) {
	if env.GOOS == "windows" {
		return FixPlan{}, errors.New("automatic COLORTERM setup is not available on Windows")
	}
	if err := ensureSafeHome(env.Home); err != nil {
		return FixPlan{}, err
	}
	shell, ok := ShellKindFromPath(env.Shell)
	if !ok {
		return FixPlan{}, errors.New("automatic setup supports Bash, zsh, and fish")
	}
	path := shell.ConfigPath(env.Home)
	unmanaged, err := unmanagedText(path)
	if err != nil {
		return FixPlan{}, err
	}
	if detail := detectColortermConflict(unmanaged, shell); detail != "" {
		return FixPlan{}, fmt.Errorf("found an existing COLORTERM assignment in %s and did not change it: %s", path, detail)
	}
	export := shell.ColortermExport()
	return FixPlan{
		ID: ColortermID, Handle: ColortermHandle, Path: path, Shell: shell, Alias: export, Kind: FixKindShell,
		Caveats: []string{
			"COLORTERM loads only in new interactive shells.",
			"Only set this when your terminal actually supports truecolor.",
		},
		SkipWrite: managedItemExact(path, ColortermID, export) || colortermHealthy(unmanaged, shell),
	}, nil
}

func ApplyColorterm(plan FixPlan) (FixOutcome, error) {
	if plan.ID != ColortermID {
		return FixOutcome{}, fmt.Errorf("unsupported colorterm fix %q", plan.ID)
	}
	if plan.SkipWrite {
		return FixOutcome{ID: plan.ID, Status: FixAlreadyConfigured, Path: plan.Path, Shell: plan.Shell, Kind: FixKindShell, Line: plan.Alias}, nil
	}
	written, err := upsertManagedItem(plan.Path, plan.ID, plan.Alias)
	if err != nil {
		return FixOutcome{}, err
	}
	if !managedItemExact(plan.Path, plan.ID, plan.Alias) {
		return FixOutcome{}, errors.New("the configuration changed, but the COLORTERM export could not be verified")
	}
	unmanaged, err := unmanagedText(plan.Path)
	if err != nil {
		return FixOutcome{}, err
	}
	if detail := detectColortermConflict(unmanaged, plan.Shell); detail != "" {
		return FixOutcome{}, errors.New("the configuration changed, but the COLORTERM export could not be verified")
	}
	status := FixAlreadyConfigured
	if written {
		status = FixApplied
	}
	return FixOutcome{ID: plan.ID, Status: status, Path: plan.Path, Shell: plan.Shell, Kind: FixKindShell, Line: plan.Alias}, nil
}

func colortermListing(env FixEnv) FixListing {
	listing := FixListing{
		ID: ColortermID, Handle: ColortermHandle, Availability: "here",
		Detail: "Export COLORTERM=truecolor in the shell startup file",
	}
	switch {
	case env.GOOS == "windows":
		listing.Availability = "unsupported"
		listing.Detail = "not available on Windows"
	case env.Shell == "":
		listing.Availability = "unsupported"
		listing.Detail = "SHELL is unset"
	default:
		if _, ok := ShellKindFromPath(env.Shell); !ok {
			listing.Availability = "unsupported"
			listing.Detail = "supports Bash, zsh, and fish"
		}
	}
	return listing
}

func detectColortermConflict(text string, shell ShellKind) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !isColortermAssignment(trimmed, shell) {
			continue
		}
		if colortermAssignmentHealthy(trimmed, shell) {
			continue
		}
		return "existing non-truecolor COLORTERM assignment"
	}
	return ""
}

func colortermHealthy(text string, shell ShellKind) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if colortermAssignmentHealthy(trimmed, shell) {
			return true
		}
	}
	return false
}

func isColortermAssignment(line string, shell ShellKind) bool {
	if shell == ShellFish {
		return strings.Contains(line, "COLORTERM") && (strings.HasPrefix(line, "set ") || strings.HasPrefix(line, "set\t"))
	}
	if strings.HasPrefix(line, "export COLORTERM=") || strings.HasPrefix(line, "COLORTERM=") {
		return true
	}
	return false
}

func colortermAssignmentHealthy(line string, shell ShellKind) bool {
	if !isColortermAssignment(line, shell) {
		return false
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "truecolor") || strings.Contains(lower, "24bit")
}
