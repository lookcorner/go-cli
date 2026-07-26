//go:build linux

package tools

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNamespaceLockdownFilterSemantics(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("filter arch gate only tested on amd64/arm64")
	}
	filter := buildNamespaceLockdownFilter()
	arch := expectedAuditArch()
	allow := uint32(seccompRetAllow)
	eperm := uint32(seccompRetErrno) | uint32(unix.EPERM)
	enosys := uint32(seccompRetErrno) | uint32(unix.ENOSYS)

	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_WRITE), 0); got != allow {
		t.Fatalf("write allow got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch^1, uint32(unix.SYS_WRITE), 0); got != eperm {
		t.Fatalf("wrong arch got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_UNSHARE), 0); got != eperm {
		t.Fatalf("unshare got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_SETNS), 0); got != eperm {
		t.Fatalf("setns got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_CLONE3), 0); got != enosys {
		t.Fatalf("clone3 got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_CLONE), uint32(unix.CLONE_NEWUSER)); got != eperm {
		t.Fatalf("clone NEWUSER got=%#x", got)
	}
	if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_CLONE), uint32(unix.SIGCHLD)); got != allow {
		t.Fatalf("clone SIGCHLD got=%#x", got)
	}
	if runtime.GOARCH == "amd64" {
		if got := evaluateNamespaceLockdownFilter(filter, arch, uint32(unix.SYS_WRITE)|x32SyscallBit, 0); got != eperm {
			t.Fatalf("x32 got=%#x", got)
		}
	}
}

func TestWrapBubblewrapWithSeccompInsertsHelper(t *testing.T) {
	args := []string{"--die-with-parent", "--", "/bin/sh"}
	wrapped := wrapBubblewrapWithSeccomp(args)
	if len(wrapped) < 4 || wrapped[len(wrapped)-2] != SeccompNamespaceMarker || wrapped[len(wrapped)-1] != "/bin/sh" {
		t.Fatalf("wrapped=%q", wrapped)
	}
	if wrapped[len(wrapped)-3] == "/bin/sh" {
		t.Fatal("helper path missing")
	}
}
