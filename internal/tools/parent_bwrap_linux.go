//go:build linux

package tools

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func ensureParentBwrapHookWriteDeny(profile, workspace, home string, args []string, warn io.Writer) error {
	_ = profile
	_ = workspace
	plan, err := BuildHookWriteDenyPlan(home)
	if err != nil {
		return err
	}
	if IsInsideBwrap() {
		return verifyHookWriteDenyEnforced(plan)
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("sandbox requires bubblewrap for hook write-deny: %w", err)
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
		"--dev-bind", "/dev", "/dev",
		"--proc", "/proc",
	}
	for _, anc := range plan.AncestorRW {
		argv = append(argv, "--bind", anc, anc)
	}
	for _, leaf := range plan.Leaves {
		argv = append(argv, "--ro-bind", leaf, leaf)
	}
	argv = append(argv, "--", self)
	argv = append(argv, args...)

	env := os.Environ()
	env = append(env, InsideBwrapEnv+"=1")
	if warn != nil {
		fmt.Fprintf(warn, "gork: re-exec under bubblewrap for hook write-deny (%d leaves)\n", len(plan.Leaves))
	}
	return syscall.Exec(bwrap, argv, env)
}

func verifyHookWriteDenyEnforced(plan HookWriteDenyPlan) error {
	for _, leaf := range plan.Leaves {
		ro, err := pathEffectivelyReadOnly(leaf)
		if err != nil {
			return fmt.Errorf("cannot verify hook write-deny path %s: %w", leaf, err)
		}
		if !ro {
			return fmt.Errorf("required hook write-deny path is not effectively read-only: %s", leaf)
		}
	}
	return nil
}

func pathEffectivelyReadOnly(path string) (bool, error) {
	var buf syscall.Statfs_t
	if err := syscall.Statfs(path, &buf); err != nil {
		return false, err
	}
	// ST_RDONLY is 1 on Linux.
	const stRDONLY = 0x1
	return buf.Flags&stRDONLY != 0, nil
}
