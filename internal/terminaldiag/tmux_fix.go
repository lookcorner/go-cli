package terminaldiag

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	TmuxClipboardID        = "terminal.tmux-clipboard"
	TmuxClipboardHandle    = "tmux-clipboard"
	DCSPassthroughID       = "terminal.dcs-passthrough"
	DCSPassthroughHandle   = "dcs-passthrough"
	TmuxExtendedKeysID     = "terminal.tmux-extended-keys"
	TmuxExtendedKeysHandle = "tmux-extended-keys"
	tmuxScannerCaveat      = "Gork checks this file for direct global assignments of this option. Review sourced files, conditionals, plugins, and generated tmux setup yourself."
)

type tmuxOptionScope int

const (
	tmuxScopeServer tmuxOptionScope = iota
	tmuxScopeWindow
)

type tmuxOptionSpec struct {
	ID            string
	Handle        string
	Option        string
	Line          string
	HealthyValues []string
	Scope         tmuxOptionScope
	Label         string
}

var (
	tmuxClipboardSpec = tmuxOptionSpec{
		ID: TmuxClipboardID, Handle: TmuxClipboardHandle, Option: "set-clipboard",
		Line: "set -g set-clipboard on", HealthyValues: []string{"on", "external"},
		Scope: tmuxScopeServer, Label: "Enable tmux clipboard forwarding",
	}
	dcsPassthroughSpec = tmuxOptionSpec{
		ID: DCSPassthroughID, Handle: DCSPassthroughHandle, Option: "allow-passthrough",
		Line: "set -wg allow-passthrough on", HealthyValues: []string{"on", "all"},
		Scope: tmuxScopeWindow, Label: "Enable tmux DCS passthrough",
	}
	tmuxExtendedKeysSpec = tmuxOptionSpec{
		ID: TmuxExtendedKeysID, Handle: TmuxExtendedKeysHandle, Option: "extended-keys",
		Line: "set -g extended-keys on", HealthyValues: []string{"on"},
		Scope: tmuxScopeServer, Label: "Enable tmux extended keys",
	}
	tmuxOptionSpecs = []*tmuxOptionSpec{&tmuxClipboardSpec, &dcsPassthroughSpec, &tmuxExtendedKeysSpec}
)

func tmuxSpecByID(id string) *tmuxOptionSpec {
	for _, spec := range tmuxOptionSpecs {
		if spec.ID == id {
			return spec
		}
	}
	return nil
}

func tmuxConfigPath(env FixEnv) (string, error) {
	if env.ByobuConfigDir != "" {
		if err := ensureSafeHome(env.ByobuConfigDir); err != nil {
			return "", fmt.Errorf("BYOBU_CONFIG_DIR: %w", err)
		}
		return filepath.Join(env.ByobuConfigDir, ".tmux.conf"), nil
	}
	return filepath.Join(env.Home, ".tmux.conf"), nil
}

func PlanTmuxOption(env FixEnv, id string) (FixPlan, error) {
	spec := tmuxSpecByID(id)
	if spec == nil {
		return FixPlan{}, fmt.Errorf("unsupported tmux fix %q", id)
	}
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
	healthy, conflict := scanDirectTmuxOption(unmanaged, spec)
	if conflict != "" {
		return FixPlan{}, fmt.Errorf("found an existing tmux customization in %s and did not change it: %s", path, conflict)
	}
	plan := FixPlan{
		ID: spec.ID, Handle: spec.Handle, Path: path, Alias: spec.Line, Kind: FixKindTmux,
		Caveats: []string{
			"The live tmux server is unchanged until you reload this config or detach and reattach.",
			tmuxScannerCaveat,
		},
		SkipWrite: healthy,
	}
	return plan, nil
}

func ApplyTmuxOption(plan FixPlan) (FixOutcome, error) {
	spec := tmuxSpecByID(plan.ID)
	if spec == nil {
		return FixOutcome{}, fmt.Errorf("unsupported tmux fix %q", plan.ID)
	}
	if plan.SkipWrite {
		return FixOutcome{ID: plan.ID, Status: FixAlreadyConfigured, Path: plan.Path, Kind: FixKindTmux, Line: spec.Line}, nil
	}
	written, err := upsertManagedItem(plan.Path, plan.ID, plan.Alias)
	if err != nil {
		return FixOutcome{}, err
	}
	if !managedItemExact(plan.Path, plan.ID, plan.Alias) {
		return FixOutcome{}, errors.New("the configuration changed, but the managed tmux option could not be verified")
	}
	unmanaged, err := unmanagedText(plan.Path)
	if err != nil {
		return FixOutcome{}, err
	}
	if _, conflict := scanDirectTmuxOption(unmanaged, spec); conflict != "" {
		return FixOutcome{}, errors.New("the configuration changed, but the managed tmux option could not be verified")
	}
	status := FixAlreadyConfigured
	if written {
		status = FixApplied
	}
	return FixOutcome{ID: plan.ID, Status: status, Path: plan.Path, Kind: FixKindTmux, Line: spec.Line}, nil
}

func tmuxListing(env FixEnv, spec *tmuxOptionSpec) FixListing {
	listing := FixListing{ID: spec.ID, Handle: spec.Handle, Availability: "here", Detail: spec.Label}
	if !env.Tmux {
		listing.Availability = "unsupported"
		listing.Detail = "run inside a tmux session"
	}
	return listing
}

func scanDirectTmuxOption(text string, spec *tmuxOptionSpec) (healthy bool, conflict string) {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		cmd := fields[0]
		scope, ok := tmuxCommandScope(cmd)
		if !ok {
			continue
		}
		global := false
		window := scope == tmuxScopeWindow
		option := ""
		value := ""
		for i := 1; i < len(fields); i++ {
			token := fields[i]
			if strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") {
				for _, flag := range token[1:] {
					switch flag {
					case 'g':
						global = true
					case 'w':
						window = true
					case 't':
						if i+1 < len(fields) {
							i++
						}
					}
				}
				continue
			}
			if option == "" {
				option = token
				continue
			}
			value = token
			break
		}
		if option != spec.Option {
			continue
		}
		effective := tmuxScopeServer
		if window {
			effective = tmuxScopeWindow
		}
		if effective != spec.Scope {
			return false, fmt.Sprintf("conflicting `%s` assignment", trimmed)
		}
		if spec.Scope == tmuxScopeServer && !global {
			return false, fmt.Sprintf("conflicting `%s` assignment", trimmed)
		}
		if value == "" {
			return false, fmt.Sprintf("ambiguous `%s` assignment", trimmed)
		}
		if containsString(spec.HealthyValues, value) {
			healthy = true
			continue
		}
		return false, fmt.Sprintf("existing `%s %s`", spec.Option, value)
	}
	return healthy, ""
}

func tmuxCommandScope(cmd string) (tmuxOptionScope, bool) {
	switch cmd {
	case "set", "set-option":
		return tmuxScopeServer, true
	case "setw", "set-window-option":
		return tmuxScopeWindow, true
	default:
		return 0, false
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
