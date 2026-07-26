//go:build linux

package voice

import (
	"errors"
	"testing"
)

func TestProbeInputLinuxRecorder(t *testing.T) {
	probe := ProbeInput(func(name string) (string, error) {
		if name == "parec" {
			return "/usr/bin/parec", nil
		}
		return "", errors.New("missing")
	})
	if !probe.Supported || probe.Name != "parec" || probe.Error != "" || probe.Detail == "" {
		t.Fatalf("probe=%#v", probe)
	}
}

func TestProbeInputLinuxMissing(t *testing.T) {
	probe := ProbeInput(func(string) (string, error) { return "", errors.New("missing") })
	if !probe.Supported || probe.Error == "" || probe.Name != "" {
		t.Fatalf("probe=%#v", probe)
	}
}
