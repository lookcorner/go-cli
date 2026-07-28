//go:build linux

package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func ensureParentBwrap(profile, workspace, home string, needsHooks, needsDeny bool, args []string, warn io.Writer) error {
	plan, err := BuildParentBwrapPlan(profile, workspace, home, needsHooks, needsDeny)
	if err != nil {
		return err
	}
	if IsInsideBwrap() {
		return verifyParentBwrapEnforced(plan)
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("sandbox requires bubblewrap for parent confinement: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable for bwrap re-exec: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve executable for bwrap re-exec: %w", err)
	}

	argv := []string{
		"bwrap",
		"--cap-drop", "ALL",
		"--bind", "/", "/",
	}
	for _, anc := range plan.Hooks.AncestorRW {
		argv = append(argv, "--bind", anc, anc)
	}
	for _, leaf := range plan.Hooks.Leaves {
		argv = append(argv, "--ro-bind", leaf, leaf)
	}
	for _, target := range plan.DenyRead {
		blocked, err := bwrapBlockedSourceForPath(home, target)
		if err != nil {
			return fmt.Errorf("sandbox deny bind-over for %s: %w", target, err)
		}
		argv = append(argv, "--ro-bind", blocked, target)
	}
	argv = append(argv,
		"--dev-bind", "/dev", "/dev",
		"--proc", "/proc",
		"--", self,
	)
	argv = append(argv, args...)

	env := os.Environ()
	env = append(env, InsideBwrapEnv+"=1")
	if warn != nil {
		fmt.Fprintf(warn, "gork: re-exec under bubblewrap (hooks=%d deny=%d)\n",
			len(plan.Hooks.Leaves), len(plan.DenyRead))
	}
	return syscall.Exec(bwrap, argv, env)
}

func verifyParentBwrapEnforced(plan ParentBwrapPlan) error {
	for _, leaf := range plan.Hooks.Leaves {
		ro, err := pathEffectivelyReadOnly(leaf)
		if err != nil {
			return fmt.Errorf("cannot verify hook write-deny path %s: %w", leaf, err)
		}
		if !ro {
			return fmt.Errorf("required hook write-deny path is not effectively read-only: %s", leaf)
		}
	}
	for _, target := range plan.DenyRead {
		if readable, err := pathEffectivelyReadable(target); err != nil {
			return fmt.Errorf("cannot verify deny path %s: %w", target, err)
		} else if readable {
			return fmt.Errorf("required deny path is still readable: %s", target)
		}
	}
	return nil
}

func pathEffectivelyReadOnly(path string) (bool, error) {
	var buf syscall.Statfs_t
	if err := syscall.Statfs(path, &buf); err != nil {
		return false, err
	}
	const stRDONLY = 0x1
	return buf.Flags&stRDONLY != 0, nil
}

func pathEffectivelyReadable(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return false, nil
		}
		// Missing after bind-over is unexpected; treat as not readable.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	_ = file.Close()
	return true, nil
}

func bwrapBlockedSourceForPath(home, target string) (string, error) {
	wantDir := false
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		wantDir = true
	}
	name := "sandbox-blocked"
	if wantDir {
		name = "sandbox-blocked-dir"
	}
	path := filepath.Join(home, fmt.Sprintf("%s.%d", name, os.Getpid()))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil {
		isDir := info.IsDir()
		if isDir != wantDir {
			if isDir {
				if err := os.RemoveAll(path); err != nil {
					return "", err
				}
			} else if err := os.Remove(path); err != nil {
				return "", err
			}
		} else {
			if err := os.Chmod(path, 0); err != nil {
				return "", err
			}
			return path, nil
		}
	}
	if wantDir {
		if err := os.Mkdir(path, 0o700); err != nil {
			return "", err
		}
	} else {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return "", err
		}
		_ = file.Close()
	}
	if err := os.Chmod(path, 0); err != nil {
		return "", err
	}
	return path, nil
}
