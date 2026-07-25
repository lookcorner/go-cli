package tui

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/config"
)

const defaultRendererFPS = 60

func RendererFPS(settings config.DisplayRefreshConfig) int {
	if fps, ok := cadenceFPSFromEnv("GROK_MIN_DRAW_MS"); ok {
		return fps
	}
	if !settings.ProbeEnabled || !settings.AutoCadenceEnabled {
		return defaultRendererFPS
	}
	if remoteDisplaySession() {
		return defaultRendererFPS
	}
	hz, ok := probeDisplayRefreshHz()
	return displayRefreshFPS(settings, hz, ok)
}

func remoteDisplaySession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" ||
		os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != ""
}

func displayRefreshFPS(settings config.DisplayRefreshConfig, hz uint32, ok bool) int {
	if !ok || hz < settings.MinHz || hz > settings.MaxHz {
		return defaultRendererFPS
	}
	ms := uint32(math.Round(1000 / float64(hz)))
	ms = min(max(ms, settings.FloorMS), settings.CeilingMS)
	return min(max(int(math.Round(1000/float64(ms))), 1), 120)
}

func cadenceFPSFromEnv(name string) (int, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	ms, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || ms < 1 {
		ms = 16
	}
	ms = min(ms, 100)
	return min(max(int(math.Round(1000/float64(ms))), 1), 120), true
}
