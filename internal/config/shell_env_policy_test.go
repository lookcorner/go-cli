package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadShellEnvironmentPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[shell_environment_policy]
inherit = "core"
ignore_default_excludes = false
exclude = ["FOO", "BAR_*"]
include_only = ["PATH", "HOME"]

[shell_environment_policy.set]
MY_FLAG = "1"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.ShellEnvironmentPolicy
	if p.Inherit != "core" || p.IgnoreDefaultExcludes || p.Set["MY_FLAG"] != "1" {
		t.Fatalf("policy=%#v", p)
	}
	if len(p.Exclude) != 2 || p.Exclude[0] != "FOO" || len(p.IncludeOnly) != 2 {
		t.Fatalf("filters=%#v", p)
	}
}

func TestLoadShellEnvironmentPolicyInvalidInheritFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[shell_environment_policy]\ninherit = \"wat\"\nignore_default_excludes = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShellEnvironmentPolicy.Inherit != "all" {
		t.Fatalf("inherit=%q", cfg.ShellEnvironmentPolicy.Inherit)
	}
	if cfg.ShellEnvironmentPolicy.IgnoreDefaultExcludes {
		t.Fatal("ignore_default_excludes should apply")
	}
}
