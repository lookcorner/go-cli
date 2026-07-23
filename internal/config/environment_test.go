package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverEnvironmentHonorsTrustAndClosestScope(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	nested := filepath.Join(root, "service")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMCPFile(t, filepath.Join(root, ".grok", "config.toml"), "[env]\nSHARED = \"root\"\nROOT = \"yes\"\n")
	writeMCPFile(t, filepath.Join(nested, ".grok", "config.toml"), "[env]\nSHARED = \"nested\"\n")
	cfg := Config{Env: map[string]string{"GLOBAL": "yes", "SHARED": "global"}}
	trusted := DiscoverEnvironment(nested, cfg, true)
	if trusted["GLOBAL"] != "yes" || trusted["ROOT"] != "yes" || trusted["SHARED"] != "nested" {
		t.Fatalf("trusted=%#v", trusted)
	}
	untrusted := DiscoverEnvironment(nested, cfg, false)
	if untrusted["SHARED"] != "global" || untrusted["ROOT"] != "" {
		t.Fatalf("untrusted=%#v", untrusted)
	}
}
