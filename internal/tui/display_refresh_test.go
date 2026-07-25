package tui

import (
	"os"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
)

func TestRendererFPSDefaultsAndEnvironmentOverride(t *testing.T) {
	settings := config.DisplayRefreshConfig{ProbeEnabled: true, AutoCadenceEnabled: false, FloorMS: 8, CeilingMS: 16, MinHz: 55, MaxHz: 165}
	t.Run("default", func(t *testing.T) {
		unsetEnv(t, "GROK_MIN_DRAW_MS")
		if got := RendererFPS(settings); got != defaultRendererFPS {
			t.Fatalf("default fps=%d", got)
		}
	})
	t.Run("empty environment value", func(t *testing.T) {
		t.Setenv("GROK_MIN_DRAW_MS", "")
		if got := RendererFPS(settings); got != 63 {
			t.Fatalf("empty environment cadence fps=%d", got)
		}
	})
	t.Run("8ms environment value", func(t *testing.T) {
		t.Setenv("GROK_MIN_DRAW_MS", "8")
		if got := RendererFPS(settings); got != 120 {
			t.Fatalf("8ms environment cadence fps=%d", got)
		}
	})
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, ok := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestDisplayRefreshFPSClampsAndRejectsProbe(t *testing.T) {
	settings := config.DisplayRefreshConfig{ProbeEnabled: true, AutoCadenceEnabled: true, FloorMS: 8, CeilingMS: 16, MinHz: 55, MaxHz: 165}
	for _, test := range []struct {
		hz   uint32
		ok   bool
		want int
	}{{120, true, 120}, {60, true, 63}, {30, true, 60}, {240, true, 60}, {0, false, 60}} {
		if got := displayRefreshFPS(settings, test.hz, test.ok); got != test.want {
			t.Fatalf("hz=%d ok=%v fps=%d want=%d", test.hz, test.ok, got, test.want)
		}
	}
}

func TestRendererFPSSkipsRemoteDisplayProbe(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "client server")
	settings := config.DisplayRefreshConfig{ProbeEnabled: true, AutoCadenceEnabled: true, FloorMS: 8, CeilingMS: 16, MinHz: 55, MaxHz: 165}
	if got := RendererFPS(settings); got != defaultRendererFPS {
		t.Fatalf("remote fps=%d", got)
	}
}
