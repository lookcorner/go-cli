package terminaldiag

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	SSHWrapID      = "terminal.ssh-wrap"
	SSHWrapHandle  = "ssh-wrap"
	SSHWrapCommand = "gork doctor fix ssh-wrap"
	SSHWrapOneOff  = "gork wrap ssh <host>"
)

type ShellKind int

const (
	ShellBash ShellKind = iota
	ShellZsh
	ShellFish
)

func (s ShellKind) Name() string {
	switch s {
	case ShellBash:
		return "bash"
	case ShellZsh:
		return "zsh"
	case ShellFish:
		return "fish"
	default:
		return "unknown"
	}
}

func (s ShellKind) Alias() string {
	switch s {
	case ShellFish:
		return "alias ssh 'gork wrap ssh'"
	default:
		return "alias ssh='gork wrap ssh'"
	}
}

func (s ShellKind) ConfigPath(home string) string {
	switch s {
	case ShellBash:
		return filepath.Join(home, ".bashrc")
	case ShellZsh:
		return filepath.Join(home, ".zshrc")
	case ShellFish:
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		return ""
	}
}

func ShellKindFromPath(shell string) (ShellKind, bool) {
	switch filepath.Base(shell) {
	case "bash":
		return ShellBash, true
	case "zsh":
		return ShellZsh, true
	case "fish":
		return ShellFish, true
	default:
		return 0, false
	}
}

type FixStatus string

const (
	FixApplied           FixStatus = "applied"
	FixAlreadyConfigured FixStatus = "already_configured"
)

type FixEnv struct {
	Home         string
	Shell        string
	GOOS         string
	SSH          bool
	VSCodeRemote bool
	Getenv       func(string) string
}

func DefaultFixEnv() FixEnv {
	home, _ := os.UserHomeDir()
	if value := strings.TrimSpace(os.Getenv("HOME")); value != "" {
		home = value
	}
	return FixEnv{
		Home: home, Shell: os.Getenv("SHELL"), GOOS: runtime.GOOS,
		SSH:          os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "",
		VSCodeRemote: os.Getenv("VSCODE_INJECTION") != "" || strings.Contains(os.Getenv("TERM_PROGRAM"), "vscode"),
		Getenv:       os.Getenv,
	}
}

func ResolveFixID(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "list":
		return "", errors.New("fix id is required")
	case SSHWrapHandle, SSHWrapID:
		return SSHWrapID, nil
	default:
		return "", fmt.Errorf("`%s` is not an available Doctor fix. Run `gork doctor fix` to list available fixes", value)
	}
}

type FixListing struct {
	ID           string
	Handle       string
	Availability string // here | run_locally | unsupported
	Detail       string
}

func ListAutomaticFixes(env FixEnv) []FixListing {
	listing := FixListing{ID: SSHWrapID, Handle: SSHWrapHandle, Availability: "here"}
	switch {
	case env.GOOS == "windows":
		listing.Availability = "unsupported"
		listing.Detail = "not available on Windows; use " + SSHWrapOneOff
	case env.VSCodeRemote:
		listing.Availability = "unsupported"
		listing.Detail = "does not apply to VS Code Remote sessions"
	case env.SSH:
		listing.Availability = "run_locally"
		listing.Detail = "run on your local computer, not in the SSH session"
	case env.Shell == "":
		listing.Availability = "unsupported"
		listing.Detail = "SHELL is unset"
	default:
		if _, ok := ShellKindFromPath(env.Shell); !ok {
			listing.Availability = "unsupported"
			listing.Detail = "supports Bash, zsh, and fish"
		}
	}
	return []FixListing{listing}
}

func FormatFixListing(listings []FixListing) string {
	if len(listings) == 0 {
		return "No automatic Doctor fixes are available."
	}
	var out strings.Builder
	out.WriteString("Automatic Doctor fixes:\n")
	for _, item := range listings {
		switch item.Availability {
		case "here":
			fmt.Fprintf(&out, "  %s  gork doctor fix %s --yes\n", item.Handle, item.Handle)
		case "run_locally":
			fmt.Fprintf(&out, "  %s  run locally: gork doctor fix %s --yes (%s)\n", item.Handle, item.Handle, item.Detail)
		default:
			fmt.Fprintf(&out, "  %s  unavailable (%s)\n", item.Handle, item.Detail)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

type FixPlan struct {
	ID      string
	Handle  string
	Path    string
	Shell   ShellKind
	Alias   string
	Caveats []string
}

type FixOutcome struct {
	ID     string
	Status FixStatus
	Path   string
	Shell  ShellKind
}

func PlanSSHWrap(env FixEnv) (FixPlan, error) {
	if env.GOOS == "windows" {
		return FixPlan{}, errors.New("automatic SSH setup is not available on Windows. Run `" + SSHWrapOneOff + "` when needed")
	}
	if env.VSCodeRemote {
		return FixPlan{}, errors.New("this fix does not apply to VS Code Remote sessions")
	}
	if env.SSH {
		return FixPlan{}, errors.New("run this fix on your local computer, not in the SSH session")
	}
	if err := ensureSafeHome(env.Home); err != nil {
		return FixPlan{}, err
	}
	shell, ok := ShellKindFromPath(env.Shell)
	if !ok {
		return FixPlan{}, errors.New("automatic setup supports Bash, zsh, and fish. For another shell, run `" + SSHWrapOneOff + "` when needed")
	}
	path := shell.ConfigPath(env.Home)
	unmanaged, err := unmanagedText(path)
	if err != nil {
		return FixPlan{}, err
	}
	if detail := detectSSHCustomization(unmanaged, shell); detail != "" {
		return FixPlan{}, fmt.Errorf("found an existing SSH alias or function in %s and did not change it: %s", path, detail)
	}
	return FixPlan{
		ID: SSHWrapID, Handle: SSHWrapHandle, Path: path, Shell: shell, Alias: shell.Alias(),
		Caveats: []string{
			"The alias loads only in new interactive shells.",
			"Use `command ssh ...` to bypass the alias.",
			"`gork wrap` starts the SSH process directly, so the alias does not loop.",
		},
	}, nil
}

func ApplySSHWrap(plan FixPlan) (FixOutcome, error) {
	written, err := upsertManagedItem(plan.Path, plan.ID, plan.Alias)
	if err != nil {
		return FixOutcome{}, err
	}
	if !managedItemExact(plan.Path, plan.ID, plan.Alias) {
		return FixOutcome{}, errors.New("the configuration changed, but the SSH alias could not be verified")
	}
	unmanaged, err := unmanagedText(plan.Path)
	if err != nil {
		return FixOutcome{}, err
	}
	if detail := detectSSHCustomization(unmanaged, plan.Shell); detail != "" {
		return FixOutcome{}, errors.New("the configuration changed, but the SSH alias could not be verified")
	}
	status := FixAlreadyConfigured
	if written {
		status = FixApplied
	}
	return FixOutcome{ID: plan.ID, Status: status, Path: plan.Path, Shell: plan.Shell}, nil
}

func FormatFixPreview(plan FixPlan) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Fix %s\n  file   %s\n  change %s\n", plan.Handle, plan.Path, plan.Alias)
	if len(plan.Caveats) > 0 {
		out.WriteString("\nNotes:\n")
		for _, caveat := range plan.Caveats {
			fmt.Fprintf(&out, "  - %s\n", caveat)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func FormatFixSuccess(outcome FixOutcome) string {
	switch outcome.Status {
	case FixApplied:
		return fmt.Sprintf("Set up SSH wrapping in %s.\nOpen a new shell or run `gork doctor` again to confirm.", outcome.Path)
	default:
		return fmt.Sprintf("SSH wrapping was already configured in %s.", outcome.Path)
	}
}

func detectSSHCustomization(text string, shell ShellKind) string {
	if shell == ShellFish {
		return detectFishSSHCustomization(text)
	}
	return detectPOSIXSSHCustomization(text)
}

func detectPOSIXSSHCustomization(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isPOSIXSSHAlias(trimmed) {
			return "existing `alias ssh=...`"
		}
		if isPOSIXSSHFunction(trimmed) {
			return "existing `ssh` shell function"
		}
	}
	return ""
}

func detectFishSSHCustomization(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "alias ssh ") || strings.HasPrefix(trimmed, "alias ssh\t") {
			return "existing `alias ssh ...`"
		}
		if strings.Contains(trimmed, "function ssh") {
			return "existing `ssh` fish function"
		}
	}
	return ""
}

func isPOSIXSSHAlias(line string) bool {
	rest, ok := afterKeyword(line, "alias")
	if !ok {
		return false
	}
	rest = strings.TrimPrefix(rest, "ssh")
	if strings.HasPrefix(rest, "=") {
		return true
	}
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, "=")
}

func isPOSIXSSHFunction(line string) bool {
	if rest, ok := afterKeyword(line, "function"); ok {
		return tokenIsName(rest, "ssh")
	}
	rest, ok := strings.CutPrefix(line, "ssh")
	if !ok {
		return false
	}
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, "()") || (strings.HasPrefix(rest, "(") && strings.HasPrefix(strings.TrimLeft(rest[1:], " \t"), ")"))
}

func afterKeyword(line, keyword string) (string, bool) {
	if !strings.HasPrefix(line, keyword) {
		return "", false
	}
	rest := line[len(keyword):]
	if rest == "" {
		return "", false
	}
	if !strings.ContainsAny(rest[:1], " \t") {
		return "", false
	}
	return strings.TrimLeft(rest, " \t"), true
}

func tokenIsName(text, name string) bool {
	if !strings.HasPrefix(text, name) {
		return false
	}
	rest := text[len(name):]
	return rest == "" || strings.ContainsAny(rest[:1], " \t({")
}
