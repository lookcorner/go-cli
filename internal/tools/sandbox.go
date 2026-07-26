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
	SandboxStrict    SandboxProfile = "strict"
)

func ParseSandboxProfile(value string) (SandboxProfile, error) {
	switch profile := SandboxProfile(strings.ToLower(strings.TrimSpace(value))); profile {
	case "", SandboxOff:
		return SandboxOff, nil
	case SandboxWorkspace, SandboxReadOnly, SandboxStrict:
		return profile, nil
	default:
		return "", fmt.Errorf("unsupported sandbox profile %q: use off, workspace, read-only, or strict", value)
	}
}

func validateSandboxRuntime(profile SandboxProfile) error {
	if profile == "" || profile == SandboxOff {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		if profile == SandboxStrict {
			return errors.New("strict sandbox requires Linux bubblewrap; macOS Seatbelt strict mode is unavailable")
		}
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
		wrapped := bubblewrapArgs(profile, workspace, executable)
		return path, append(wrapped, args...), nil
	default:
		return "", nil, fmt.Errorf("sandbox profiles are not supported on %s", runtime.GOOS)
	}
}

func bubblewrapArgs(profile SandboxProfile, workspace, executable string) []string {
	wrapped := []string{"--die-with-parent", "--new-session"}
	readable, writable := sandboxReadablePaths(profile, workspace), sandboxWritableMountPaths(profile, workspace)
	if profile == SandboxStrict {
		wrapped = append(wrapped, "--tmpfs", "/")
		for _, path := range sandboxParentDirs(append(append([]string(nil), readable...), writable...)) {
			wrapped = append(wrapped, "--dir", path)
		}
		for _, allowed := range readable {
			if allowed != "/dev" && allowed != "/proc" {
				wrapped = append(wrapped, "--ro-bind", allowed, allowed)
			}
		}
		wrapped = append(wrapped, "--dev", "/dev", "--proc", "/proc")
	} else {
		wrapped = append(wrapped, "--ro-bind", "/", "/", "--dev-bind", "/dev", "/dev", "--proc", "/proc")
	}
	if sandboxRestrictsNetwork(profile) {
		wrapped = append(wrapped, "--unshare-net")
	}
	for _, allowed := range writable {
		if info, err := os.Stat(allowed); err == nil && info.IsDir() {
			wrapped = append(wrapped, "--bind", allowed, allowed)
		}
	}
	if profile == SandboxStrict {
		wrapped = append(wrapped, "--remount-ro", "/")
	}
	return append(wrapped, "--", executable)
}

func sandboxParentDirs(paths []string) []string {
	seen := map[string]bool{"/": true}
	var result []string
	for _, path := range paths {
		var parents []string
		for parent := filepath.Dir(path); !seen[parent]; parent = filepath.Dir(parent) {
			parents = append(parents, parent)
		}
		for index := len(parents) - 1; index >= 0; index-- {
			parent := parents[index]
			if !seen[parent] {
				seen[parent] = true
				result = append(result, parent)
			}
		}
	}
	return result
}

func seatbeltPolicy(profile SandboxProfile, workspace string) (string, error) {
	writeFilters, err := seatbeltSubpaths(sandboxWritablePaths(profile, workspace))
	if err != nil {
		return "", err
	}
	return `(version 1)
(deny default)
(allow process*)
(allow file-read*)
(allow file-write* ` + strings.Join(writeFilters, " ") + `)
(allow file-write* (literal "/dev/null") (literal "/dev/tty"))
(allow network*)
(allow sysctl-read)
(allow mach-lookup)
`, nil
}

func seatbeltSubpaths(paths []string) ([]string, error) {
	filters := make([]string, 0, len(paths))
	for _, path := range paths {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		escaped, err := seatbeltPath(path)
		if err != nil {
			return nil, err
		}
		filters = append(filters, `(subpath "`+escaped+`")`)
	}
	return filters, nil
}

func sandboxWritablePaths(profile SandboxProfile, workspace string) []string {
	paths := sandboxWritableMountPaths(profile, workspace)
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
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

func sandboxWritableMountPaths(profile SandboxProfile, workspace string) []string {
	paths := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}
	if profile == SandboxWorkspace || profile == SandboxStrict {
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
		path = filepath.Clean(path)
		if path != "." && !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func sandboxReadablePaths(profile SandboxProfile, workspace string) []string {
	if profile != SandboxStrict {
		return nil
	}
	paths := []string{
		"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc", "/dev", "/proc", "/sys",
		"/tmp", "/run", "/var", "/System", "/Library", "/private", workspace,
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "Library"))
	}
	paths = append(paths, sandboxWritableMountPaths(profile, workspace)...)
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func sandboxRestrictsNetwork(profile SandboxProfile) bool {
	return profile == SandboxReadOnly || profile == SandboxStrict
}

func seatbeltPath(path string) (string, error) {
	for _, char := range path {
		if unicode.IsControl(char) {
			return "", errors.New("sandbox path contains a control character")
		}
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path), nil
}
