package main

import (
	"os/exec"
	"runtime"
)

func openBrowser(rawURL string) bool {
	command, args := browserCommand(runtime.GOOS, rawURL)
	if command == "" {
		return false
	}
	process := exec.Command(command, args...)
	if err := process.Start(); err != nil {
		return false
	}
	return process.Process.Release() == nil
}

func browserCommand(goos, rawURL string) (string, []string) {
	if rawURL == "" {
		return "", nil
	}
	switch goos {
	case "darwin":
		return "open", []string{rawURL}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return "xdg-open", []string{rawURL}
	}
}
