package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	resolved, err := resolveBuiltinSandboxProfile(profile, workspace)
	if err != nil {
		return parentLandlockPaths{}
	}
	return ParentLandlockPathsFromResolved(resolved, workspace)
}

// ParentLandlockPathsFromResolved builds Landlock allowlists from a resolved profile
// (built-in or custom sandbox.toml extras).
func ParentLandlockPathsFromResolved(resolved ResolvedSandboxProfile, workspace string) parentLandlockPaths {
	if resolved.ChildBase == "" || resolved.ChildBase == SandboxOff {
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
	writable := append([]string(nil), resolved.ReadWrite...)
	for _, path := range writable {
		_ = os.MkdirAll(path, 0o700)
	}
	paths.RWDirs = existingDirs(writable)

	if resolved.DefaultRead {
		paths.RODirs = []string{"/"}
	} else {
		paths.RODirs = existingDirs(resolved.ReadOnly)
	}
	// Custom / explicit read_only extras are always granted as RO dirs when present.
	for _, path := range resolved.ReadOnly {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths.RODirs = appendUnique(paths.RODirs, filepath.Clean(path))
		}
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
// profile is non-off. Unsupported kernels warn and continue for built-ins
// (Rust parity). Custom sandbox.toml profiles fail closed when Landlock cannot
// be applied and the process is not already inside parent bwrap.
// Non-Linux hosts are no-ops.
func ApplyParentLandlock(profile string, workspace string, warn io.Writer) error {
	resolved, err := ResolveSandboxProfile(profile, workspace)
	if err != nil {
		return err
	}
	if resolved.ChildBase == SandboxOff {
		return nil
	}
	applied, err := applyParentLandlockResolved(resolved, workspace, warn)
	if err != nil {
		return err
	}
	if resolved.Custom && !applied && !IsInsideBwrap() && runtime.GOOS == "linux" {
		return fmt.Errorf("custom sandbox profile %q requires parent Landlock (or bwrap); refuse to start unprotected", resolved.Name)
	}
	return nil
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
