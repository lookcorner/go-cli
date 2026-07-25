package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates tests from the developer's global and system git
// configuration so identity, CRLF conversion, and default-branch settings
// stay deterministic on every machine.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gork-test-gitconfig")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	config := "[user]\n\tname = Gork Test\n\temail = gork-test@example.invalid\n" +
		"[init]\n\tdefaultBranch = main\n[core]\n\tautocrlf = false\n"
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", path)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}
