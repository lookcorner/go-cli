package terminaldiag

import (
	"errors"
	"fmt"
	"strings"
)

const (
	TmuxTruecolorID     = "terminal.tmux-truecolor"
	TmuxTruecolorHandle = "tmux-truecolor"
	tmuxTruecolorBody   = "set -g default-terminal \"tmux-256color\"\nset -as terminal-features \",*:RGB\""
)

func PlanTmuxTruecolor(env FixEnv) (FixPlan, error) {
	if !env.Tmux {
		return FixPlan{}, errors.New("this fix applies inside a tmux session")
	}
	if err := ensureSafeHome(env.Home); err != nil {
		return FixPlan{}, err
	}
	path, err := tmuxConfigPath(env)
	if err != nil {
		return FixPlan{}, err
	}
	unmanaged, err := unmanagedText(path)
	if err != nil {
		return FixPlan{}, err
	}
	if conflict := scanTmuxTruecolorConflict(unmanaged); conflict != "" {
		return FixPlan{}, fmt.Errorf("found an existing tmux customization in %s and did not change it: %s", path, conflict)
	}
	plan := FixPlan{
		ID: TmuxTruecolorID, Handle: TmuxTruecolorHandle, Path: path,
		Alias: tmuxTruecolorBody, Kind: FixKindTmux,
		Caveats: []string{
			"The live tmux server is unchanged until you reload this config or detach and reattach.",
			"Also export COLORTERM=truecolor in your shell startup file when your terminal supports it.",
			tmuxScannerCaveat,
		},
		SkipWrite: managedItemExact(path, TmuxTruecolorID, tmuxTruecolorBody) || tmuxTruecolorHealthy(unmanaged),
	}
	return plan, nil
}

func ApplyTmuxTruecolor(plan FixPlan) (FixOutcome, error) {
	if plan.ID != TmuxTruecolorID {
		return FixOutcome{}, fmt.Errorf("unsupported tmux fix %q", plan.ID)
	}
	if plan.SkipWrite {
		return FixOutcome{ID: plan.ID, Status: FixAlreadyConfigured, Path: plan.Path, Kind: FixKindTmux, Line: tmuxTruecolorBody}, nil
	}
	written, err := upsertManagedItem(plan.Path, plan.ID, tmuxTruecolorBody)
	if err != nil {
		return FixOutcome{}, err
	}
	if !managedItemExact(plan.Path, plan.ID, tmuxTruecolorBody) {
		return FixOutcome{}, errors.New("the configuration changed, but the managed tmux truecolor options could not be verified")
	}
	unmanaged, err := unmanagedText(plan.Path)
	if err != nil {
		return FixOutcome{}, err
	}
	if conflict := scanTmuxTruecolorConflict(unmanaged); conflict != "" {
		return FixOutcome{}, errors.New("the configuration changed, but the managed tmux truecolor options could not be verified")
	}
	status := FixAlreadyConfigured
	if written {
		status = FixApplied
	}
	return FixOutcome{ID: plan.ID, Status: status, Path: plan.Path, Kind: FixKindTmux, Line: tmuxTruecolorBody}, nil
}

func tmuxTruecolorListing(env FixEnv) FixListing {
	listing := FixListing{
		ID: TmuxTruecolorID, Handle: TmuxTruecolorHandle, Availability: "here",
		Detail: "Enable tmux 256-color / truecolor terminal features",
	}
	if !env.Tmux {
		listing.Availability = "unsupported"
		listing.Detail = "run inside a tmux session"
	}
	return listing
}

func scanTmuxTruecolorConflict(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		option, value, ok := parseTmuxAssignment(trimmed)
		if !ok {
			continue
		}
		switch option {
		case "default-terminal":
			if !tmuxDefaultTerminalHealthy(value) {
				return fmt.Sprintf("existing `default-terminal %s`", value)
			}
		case "terminal-features":
			if !tmuxTerminalFeaturesHealthy(value) {
				return fmt.Sprintf("existing `terminal-features %s`", value)
			}
		}
	}
	return ""
}

func tmuxTruecolorHealthy(text string) bool {
	defaultOK, featuresOK := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		option, value, ok := parseTmuxAssignment(trimmed)
		if !ok {
			continue
		}
		switch option {
		case "default-terminal":
			defaultOK = tmuxDefaultTerminalHealthy(value)
		case "terminal-features":
			featuresOK = tmuxTerminalFeaturesHealthy(value)
		}
	}
	return defaultOK && featuresOK
}

func tmuxDefaultTerminalHealthy(value string) bool {
	switch strings.Trim(value, `"'`) {
	case "tmux-256color", "screen-256color":
		return true
	default:
		return false
	}
}

func tmuxTerminalFeaturesHealthy(value string) bool {
	normalized := strings.ToLower(strings.Trim(value, `"'`))
	return strings.Contains(normalized, "rgb") || strings.Contains(normalized, "truecolor")
}

func parseTmuxAssignment(line string) (option, value string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", "", false
	}
	if _, known := tmuxCommandScope(fields[0]); !known {
		return "", "", false
	}
	for i := 1; i < len(fields); i++ {
		token := fields[i]
		if strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") {
			for _, flag := range token[1:] {
				if flag == 't' && i+1 < len(fields) {
					i++
				}
			}
			continue
		}
		if option == "" {
			option = token
			continue
		}
		value = token
		return option, value, true
	}
	return "", "", false
}
