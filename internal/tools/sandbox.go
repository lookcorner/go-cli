package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

type SandboxProfile string

const (
	SandboxOff       SandboxProfile = "off"
	SandboxWorkspace SandboxProfile = "workspace"
	SandboxReadOnly  SandboxProfile = "read-only"
)

func ParseSandboxProfile(value string) (SandboxProfile, error) {
	switch profile := SandboxProfile(strings.ToLower(strings.TrimSpace(value))); profile {
	case "", SandboxOff:
		return SandboxOff, nil
	case SandboxWorkspace, SandboxReadOnly:
		return profile, nil
	default:
		return "", fmt.Errorf("unsupported sandbox profile %q: use off, workspace, or read-only", value)
	}
}

func validateSandboxRuntime(profile SandboxProfile) error {
	if profile == "" || profile == SandboxOff {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return errors.New("sandbox profile requires sandbox-exec on macOS")
		}
	case "linux":
		if _, err := exec.LookPath("bwrap"); err != nil {
			return errors.New("sandbox profile requires bubblewrap (bwrap) on Linux")
		}
	default:
		return fmt.Errorf("sandbox profiles are not supported on %s", runtime.GOOS)
	}
	return nil
}

func sandboxCommand(ctx context.Context, profile SandboxProfile, workspace, executable string, args ...string) (*exec.Cmd, error) {
	path, wrapped, err := sandboxInvocation(profile, workspace, executable, args)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		return exec.CommandContext(ctx, path, wrapped...), nil
	}
	return exec.Command(path, wrapped...), nil
}

func sandboxInvocation(profile SandboxProfile, workspace, executable string, args []string) (string, []string, error) {
	if profile == "" || profile == SandboxOff {
		return executable, args, nil
	}
	if err := validateSandboxRuntime(profile); err != nil {
		return "", nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		path, _ := exec.LookPath("sandbox-exec")
		policy, err := seatbeltPolicy(profile, workspace)
		if err != nil {
			return "", nil, err
		}
		return path, append([]string{"-p", policy, executable}, args...), nil
	case "linux":
		path, _ := exec.LookPath("bwrap")
		wrapped := []string{"--die-with-parent", "--new-session", "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev", "--proc", "/proc"}
		for _, allowed := range sandboxWritablePaths(profile, workspace) {
			if info, err := os.Stat(allowed); err == nil && info.IsDir() {
				wrapped = append(wrapped, "--bind", allowed, allowed)
			}
		}
		wrapped = append(wrapped, "--", executable)
		return path, append(wrapped, args...), nil
	default:
		return "", nil, fmt.Errorf("sandbox profiles are not supported on %s", runtime.GOOS)
	}
}

func seatbeltPolicy(profile SandboxProfile, workspace string) (string, error) {
	paths := sandboxWritablePaths(profile, workspace)
	filters := make([]string, 0, len(paths))
	for _, path := range paths {
		escaped, err := seatbeltPath(path)
		if err != nil {
			return "", err
		}
		filters = append(filters, `(subpath "`+escaped+`")`)
	}
	return `(version 1)
(deny default)
(allow process*)
(allow file-read*)
(allow file-write* ` + strings.Join(filters, " ") + `)
(allow file-write* (literal "/dev/null") (literal "/dev/tty"))
(allow network*)
(allow sysctl-read)
(allow mach-lookup)
`, nil
}

func sandboxWritablePaths(profile SandboxProfile, workspace string) []string {
	paths := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}
	if profile == SandboxWorkspace {
		paths = append(paths, workspace)
	}
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		paths = append(paths, home)
	} else if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".grok"))
	}
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		path = filepath.Clean(path)
		if path != "." && !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func seatbeltPath(path string) (string, error) {
	for _, char := range path {
		if unicode.IsControl(char) {
			return "", errors.New("sandbox path contains a control character")
		}
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path), nil
}
