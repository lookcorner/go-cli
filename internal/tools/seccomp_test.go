package tools

import "testing"

func TestMaybeExecSeccompNamespaceLockdownIgnoresNormalArgs(t *testing.T) {
	if MaybeExecSeccompNamespaceLockdown([]string{"gork", "version"}) {
		t.Fatal("expected false for normal argv")
	}
	if MaybeExecSeccompNamespaceLockdown(nil) {
		t.Fatal("expected false for nil argv")
	}
}
