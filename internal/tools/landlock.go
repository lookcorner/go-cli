package tools

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// parentLandlockPaths is the FS allowlist for parent-process Landlock.
type parentLandlockPaths struct {
	RODirs  []string
	RWDirs  []string
	ROFiles []string
}

var parentLandlockDeviceFiles = []string{
	"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx",
}

var parentLandlockDeviceDirs = []string{"/dev/pts", "/dev/fd"}

// ParentLandlockPathsFor builds RO/RW path sets matching Rust built-in profiles.
func ParentLandlockPathsFor(profile SandboxProfile, workspace string) parentLandlockPaths {
	if profile == "" || profile == SandboxOff {
		return parentLandlockPaths{}
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	var paths parentLandlockPaths
	writable := existingDirs(sandboxWritableMountPaths(profile, workspace))
	for _, path := range writable {
		_ = os.MkdirAll(path, 0o700)
	}
	paths.RWDirs = existingDirs(writable)

	switch profile {
	case SandboxStrict:
		paths.RODirs = existingDirs(sandboxReadablePaths(profile, workspace))
	default:
		// workspace and read-only: default_read = /
		paths.RODirs = []string{"/"}
	}

	for _, path := range parentLandlockDeviceDirs {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths.RODirs = appendUnique(paths.RODirs, path)
		}
	}
	for _, path := range parentLandlockDeviceFiles {
		if openableFile(path) {
			paths.ROFiles = append(paths.ROFiles, path)
		}
	}
	return paths
}

// ApplyParentLandlock confines the current process with Linux Landlock when
// profile is non-off. Unsupported kernels warn and continue (Rust parity).
// Non-Linux hosts are no-ops. Never fail-closed for built-in profiles.
func ApplyParentLandlock(profile string, workspace string, warn io.Writer) error {
	parsed, err := ParseSandboxProfile(profile)
	if err != nil {
		return err
	}
	if parsed == SandboxOff {
		return nil
	}
	return applyParentLandlock(parsed, workspace, warn)
}

func existingDirs(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if path == "" || path == "." || seen[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	return result
}

func appendUnique(paths []string, path string) []string {
	path = filepath.Clean(path)
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

func openableFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}
