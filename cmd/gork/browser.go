package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openBrowser(rawURL string) bool {
	if !browserOpenLikelyAvailable(runtime.GOOS, os.Getenv) {
		return false
	}
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

func browserOpenLikelyAvailable(goos string, getenv func(string) string) bool {
	if goos == "darwin" || goos == "windows" {
		return true
	}
	for _, name := range []string{"BROWSER", "WAYLAND_DISPLAY", "DISPLAY"} {
		if strings.TrimSpace(getenv(name)) != "" {
			return true
		}
	}
	return false
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
