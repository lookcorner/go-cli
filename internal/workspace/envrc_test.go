package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvrcRequiresTrustAndCurrentRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "nested")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTrustFile(t, filepath.Join(parent, ".envrc"), "export FROM_PARENT=yes\n")
	marker := filepath.Join(root, "executed")
	writeTrustFile(t, filepath.Join(root, ".envrc"), "touch executed\nexport LOCAL=yes\n")

	if got := LoadEnvrc(root, false); len(got) != 0 {
		t.Fatalf("untrusted environment=%#v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untrusted .envrc executed: %v", err)
	}
	if err := os.Remove(filepath.Join(root, ".envrc")); err != nil {
		t.Fatal(err)
	}
	if got := LoadEnvrc(root, true); len(got) != 0 {
		t.Fatalf("parent .envrc loaded=%#v", got)
	}
}

func TestLoadEnvrcViaBash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GORK_ENVRC_UNCHANGED", "same")
	t.Setenv("GORK_ENVRC_EMPTY_UNCHANGED", "")
	writeTrustFile(t, filepath.Join(root, "local.env"), "export SOURCED=value\n")
	writeTrustFile(t, filepath.Join(root, ".envrc"), strings.Join([]string{
		"export SIMPLE=ok",
		"export WORKSPACE_PATH=\"$PWD/subdir\"",
		"export GORK_ENVRC_UNCHANGED=same",
		"export GORK_ENVRC_EMPTY_UNCHANGED=",
		"export GORK_ENVRC_EMPTY_NEW=",
		"PATH_add bin",
		"source_env_if_exists local.env",
	}, "\n"))

	env := loadEnvrcWithBash(root, filepath.Join(root, ".envrc"))
	if env["SIMPLE"] != "ok" || env["WORKSPACE_PATH"] != filepath.Join(root, "subdir") || env["SOURCED"] != "value" {
		t.Fatalf("environment=%#v", env)
	}
	if !strings.HasPrefix(env["PATH"], filepath.Join(root, "bin")+":") {
		t.Fatalf("PATH=%q", env["PATH"])
	}
	if _, ok := env["GORK_ENVRC_UNCHANGED"]; ok {
		t.Fatalf("unchanged variable returned: %#v", env)
	}
	if _, ok := env["GORK_ENVRC_EMPTY_UNCHANGED"]; ok {
		t.Fatalf("unchanged empty variable returned: %#v", env)
	}
	if value, ok := env["GORK_ENVRC_EMPTY_NEW"]; !ok || value != "" {
		t.Fatalf("new empty variable missing: %#v", env)
	}
	for _, key := range []string{"_", "SHLVL", "PWD", "OLDPWD"} {
		if _, ok := env[key]; ok {
			t.Fatalf("internal variable %q returned: %#v", key, env)
		}
	}
}

func TestLoadEnvrcFailureReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	writeTrustFile(t, filepath.Join(root, ".envrc"), "false\nexport AFTER=no\n")
	if got := loadEnvrcWithBash(root, filepath.Join(root, ".envrc")); len(got) != 0 {
		t.Fatalf("failed .envrc environment=%#v", got)
	}
}

func TestChangedJSONEnvironment(t *testing.T) {
	value := "set"
	values := map[string]*string{"VALUE": &value, "REMOVED": nil, "PWD": &value}
	got := changedJSONEnvironment(values)
	if len(got) != 1 || got["VALUE"] != value {
		t.Fatalf("environment=%#v", got)
	}
}
