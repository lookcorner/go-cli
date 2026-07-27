package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InsideBwrapEnv marks that this process already runs under parent bwrap.
const InsideBwrapEnv = "__GROK_INSIDE_BWRAP"

// IsInsideBwrap reports whether the process was re-exec'd under parent bwrap.
func IsInsideBwrap() bool {
	return os.Getenv(InsideBwrapEnv) == "1"
}

// RequiresHookWriteDeny is true for built-in enforcing profiles (not off).
// Matches Rust profile_enforces_hook_write_deny for Go's profile set.
func RequiresHookWriteDeny(profile string) bool {
	parsed, err := ParseSandboxProfile(profile)
	if err != nil || parsed == SandboxOff {
		return false
	}
	return true
}

// HookWriteDenyPlan is the bwrap bind plan for protecting hook sources.
type HookWriteDenyPlan struct {
	AncestorRW []string
	Leaves     []string
}

// EnsureParentBwrapHookWriteDeny applies Linux parent bwrap re-exec so global
// hook sources are mounted read-only. On success of re-exec this never returns.
// Non-Linux hosts only ensure hook slots exist. Fail-closed when the profile
// requires protection and bwrap cannot be applied/verified.
func EnsureParentBwrapHookWriteDeny(profile, workspace string, args []string, warn io.Writer) error {
	if !RequiresHookWriteDeny(profile) {
		return nil
	}
	home, err := resolveGrokHome()
	if err != nil {
		return fmt.Errorf("hook write-deny: %w", err)
	}
	if err := ensureGrokHookSlots(home); err != nil {
		return fmt.Errorf("hook write-deny: %w", err)
	}
	return ensureParentBwrapHookWriteDeny(profile, workspace, home, args, warn)
}

func resolveGrokHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Clean(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok"), nil
}

func ensureGrokHookSlots(home string) error {
	hooksDir := filepath.Join(home, "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return err
	}
	// Touch hooks-paths so the file leaf exists for identity capture.
	pathsFile := filepath.Join(home, "hooks-paths")
	if _, err := os.Stat(pathsFile); os.IsNotExist(err) {
		if err := os.WriteFile(pathsFile, nil, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// BuildHookWriteDenyPlan resolves global hook leaves under GROK_HOME.
func BuildHookWriteDenyPlan(home string) (HookWriteDenyPlan, error) {
	home = filepath.Clean(home)
	var leaves []string
	seen := map[string]bool{}
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || seen[path] {
			return
		}
		if _, err := os.Lstat(path); err != nil {
			return
		}
		seen[path] = true
		leaves = append(leaves, path)
	}

	hooksDir := filepath.Join(home, "hooks")
	add(hooksDir)
	if entries, err := os.ReadDir(hooksDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if entry.Type().IsRegular() && strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
				add(filepath.Join(hooksDir, name))
			}
		}
	}
	pathsFile := filepath.Join(home, "hooks-paths")
	add(pathsFile)
	if data, err := os.ReadFile(pathsFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			add(line)
			if info, err := os.Stat(line); err == nil && info.IsDir() {
				if entries, err := os.ReadDir(line); err == nil {
					for _, entry := range entries {
						name := entry.Name()
						if entry.Type().IsRegular() && strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
							add(filepath.Join(line, name))
						}
					}
				}
			}
		}
	}
	if len(leaves) == 0 {
		return HookWriteDenyPlan{}, fmt.Errorf("no hook write-deny leaves under %s", home)
	}

	leafSet := map[string]bool{}
	for _, leaf := range leaves {
		leafSet[leaf] = true
	}
	var ancestors []string
	ancSeen := map[string]bool{}
	for _, leaf := range leaves {
		dir := filepath.Dir(leaf)
		for dir != "" && dir != "/" && dir != "." {
			if leafSet[dir] || ancSeen[dir] {
				dir = filepath.Dir(dir)
				continue
			}
			if info, err := os.Lstat(dir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				ancSeen[dir] = true
				ancestors = append(ancestors, dir)
			}
			dir = filepath.Dir(dir)
		}
	}
	return HookWriteDenyPlan{AncestorRW: ancestors, Leaves: leaves}, nil
}
