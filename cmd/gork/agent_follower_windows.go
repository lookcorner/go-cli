//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func startAgentLeader(root string, args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(root, "leader.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = log.Close()
		return err
	}
	childArgs := append([]string{"agent"}, args[1:]...)
	childArgs = append(childArgs, "leader", "--no-exit-on-disconnect")
	command := exec.Command(executable, childArgs...)
	command.Stdin, command.Stdout, command.Stderr = null, null, log
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	err = command.Start()
	return errors.Join(err, null.Close(), log.Close())
}
