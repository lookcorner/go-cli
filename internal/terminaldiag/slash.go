package terminaldiag

import (
	"strings"
)

const SlashUsage = "Usage: /doctor [fix [ssh-wrap|tmux-clipboard|dcs-passthrough|tmux-extended-keys] [--yes]]"

// HandleCommand handles /doctor and its aliases. ok is false when prompt is not a doctor command.
func HandleCommand(prompt string) (string, bool) {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return "", false
	}
	switch fields[0] {
	case "/doctor", "/terminal-setup", "/terminal-check", "/terminal-info":
	default:
		return "", false
	}
	args := fields[1:]
	if len(args) == 0 {
		return Report(), true
	}
	if args[0] != "fix" {
		if fields[0] != "/doctor" {
			return Report(), true
		}
		return SlashUsage, true
	}
	yes, rest := parseSlashFixArgs(args[1:])
	env := DefaultFixEnv()
	if len(rest) == 0 {
		if yes {
			return "--yes requires a fix id\n" + SlashUsage, true
		}
		return FormatFixListing(ListAutomaticFixes(env)), true
	}
	if len(rest) != 1 {
		return SlashUsage, true
	}
	id, err := ResolveFixID(rest[0])
	if err != nil {
		return err.Error() + "\n" + SlashUsage, true
	}
	plan, err := PlanFix(env, id)
	if err != nil {
		return err.Error(), true
	}
	if !yes {
		handle := plan.Handle
		return FormatFixPreview(plan) + "\n\nRe-run with `/doctor fix " + handle + " --yes` to apply.", true
	}
	outcome, err := ApplyFix(plan)
	if err != nil {
		return err.Error(), true
	}
	return FormatFixSuccess(outcome), true
}

func parseSlashFixArgs(args []string) (yes bool, rest []string) {
	for _, arg := range args {
		if arg == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, arg)
	}
	return yes, rest
}

// SuggestFixArgs returns slash-completion insert texts for doctor fix arguments.
func SuggestFixArgs(argsQuery string) []string {
	query := strings.TrimSpace(argsQuery)
	if query == "" {
		return nil
	}
	if query == "fix" || strings.HasPrefix(query, "fix ") {
		value := strings.TrimSpace(strings.TrimPrefix(query, "fix"))
		if value != "" {
			if _, err := ResolveFixID(value); err == nil {
				return nil
			}
		}
		var out []string
		for _, handle := range []string{SSHWrapHandle, TmuxClipboardHandle, DCSPassthroughHandle, TmuxExtendedKeysHandle} {
			if value == "" || strings.Contains(handle, value) || strings.HasPrefix("terminal."+handle, value) {
				out = append(out, "fix "+handle)
			}
		}
		return out
	}
	if strings.HasPrefix("fix", query) {
		return []string{"fix"}
	}
	return nil
}
