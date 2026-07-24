package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChannelUsesCachedStableVersion(t *testing.T) {
	current := Current
	t.Cleanup(func() { Current = current })
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)

	for _, test := range []struct {
		current string
		cache   string
		want    string
	}{
		{"0.1.2-alpha.1", `{"stable_version":"0.1.1"}`, "alpha"},
		{"0.1.1", `{"stable_version":"0.1.1"}`, "stable"},
		{"0.1.0", `{"stable_version":"0.1.1"}`, "stable"},
		{"0.1.0", `{}`, "unknown"},
		{"invalid", `{"stable_version":"0.1.1"}`, "unknown"},
	} {
		Current = test.current
		if err := os.WriteFile(filepath.Join(home, "version.json"), []byte(test.cache), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Channel(); got != test.want {
			t.Fatalf("current=%q cache=%s channel=%q want=%q", test.current, test.cache, got, test.want)
		}
	}
}

func TestChannelIsUnknownWithoutCache(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	if got := Channel(); got != "unknown" {
		t.Fatalf("channel=%q", got)
	}
}
