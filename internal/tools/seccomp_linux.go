//go:build linux

package tools

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	seccompRetAllow = 0x7fff0000
	seccompRetErrno = 0x00050000
	seccompOffNR    = 0
	seccompOffArch  = 4
	seccompOffArg0  = 16
	x32SyscallBit   = 0x40000000
)

var cloneNamespaceBits = uint32(unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS |
	unix.CLONE_NEWIPC | unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWTIME)

func expectedAuditArch() uint32 {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	default:
		return 0
	}
}

func bpfStmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// buildNamespaceLockdownFilter mirrors Rust xai-grok-sandbox child_net ns_lockdown.
func buildNamespaceLockdownFilter() []unix.SockFilter {
	ld := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jeq := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	jset := uint16(unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K)
	ret := uint16(unix.BPF_RET | unix.BPF_K)
	arch := expectedAuditArch()

	filter := make([]unix.SockFilter, 0, 22)
	filter = append(filter, bpfStmt(ld, seccompOffArch))
	filter = append(filter, bpfJump(jeq, arch, 1, 0))
	filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.EPERM)))
	filter = append(filter, bpfStmt(ld, seccompOffNR))
	if runtime.GOARCH == "amd64" {
		filter = append(filter, bpfJump(jset, x32SyscallBit, 0, 1))
		filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.EPERM)))
	}
	for _, sys := range []uint32{uint32(unix.SYS_UNSHARE), uint32(unix.SYS_SETNS)} {
		filter = append(filter, bpfJump(jeq, sys, 0, 1))
		filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.EPERM)))
	}
	filter = append(filter, bpfJump(jeq, uint32(unix.SYS_CLONE3), 0, 1))
	filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.ENOSYS)))
	filter = append(filter, bpfJump(jeq, uint32(unix.SYS_CLONE), 0, 3))
	filter = append(filter, bpfStmt(ld, seccompOffArg0))
	filter = append(filter, bpfJump(jset, cloneNamespaceBits, 0, 1))
	filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.EPERM)))
	filter = append(filter, bpfStmt(ret, seccompRetAllow))
	return filter
}

// buildChildNetworkFilter mirrors Rust install_child_network_filter.
// Blocks connect/bind/sendto/sendmsg/listen/accept/accept4 with EPERM.
func buildChildNetworkFilter() []unix.SockFilter {
	ld := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jeq := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	ret := uint16(unix.BPF_RET | unix.BPF_K)
	blocked := []uint32{
		uint32(unix.SYS_CONNECT),
		uint32(unix.SYS_BIND),
		uint32(unix.SYS_SENDTO),
		uint32(unix.SYS_SENDMSG),
		uint32(unix.SYS_LISTEN),
		uint32(unix.SYS_ACCEPT),
		uint32(unix.SYS_ACCEPT4),
	}
	filter := make([]unix.SockFilter, 0, len(blocked)+3)
	filter = append(filter, bpfStmt(ld, seccompOffNR))
	for i, sys := range blocked {
		remaining := len(blocked) - i - 1
		filter = append(filter, bpfJump(jeq, sys, uint8(remaining+1), 0))
	}
	filter = append(filter, bpfStmt(ret, seccompRetAllow))
	filter = append(filter, bpfStmt(ret, seccompRetErrno|uint32(unix.EPERM)))
	return filter
}

func installSeccompFilter(filter []unix.SockFilter) error {
	if len(filter) == 0 {
		return nil
	}
	prog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}
	rc, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if rc == 0 {
		return nil
	}
	if int(rc) > 0 {
		return fmt.Errorf("seccomp TSYNC failed: thread %d could not install filter", rc)
	}
	if errno != 0 {
		return errno
	}
	return fmt.Errorf("seccomp install failed")
}

func installNamespaceLockdownFilter() error {
	if expectedAuditArch() == 0 {
		return fmt.Errorf("seccomp namespace lockdown unsupported on %s", runtime.GOARCH)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", err)
	}
	return installSeccompFilter(buildNamespaceLockdownFilter())
}

func installChildNetworkFilter() error {
	// NO_NEW_PRIVS must already be set by the namespace filter install.
	return installSeccompFilter(buildChildNetworkFilter())
}

// MaybeExecSeccompNamespaceLockdown installs seccomp filter(s) and execs the
// real command when argv matches a bwrap helper marker. Returns false otherwise.
func MaybeExecSeccompNamespaceLockdown(args []string) bool {
	if len(args) < 3 {
		return false
	}
	restrictNet := false
	switch args[1] {
	case SeccompNamespaceMarker:
	case SeccompNamespaceNetMarker:
		restrictNet = true
	default:
		return false
	}
	if err := installNamespaceLockdownFilter(); err != nil {
		fmt.Fprintf(os.Stderr, "gork: seccomp namespace lockdown: %v\n", err)
		os.Exit(1)
	}
	if restrictNet {
		if err := installChildNetworkFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "gork: seccomp network filter: %v\n", err)
			os.Exit(1)
		}
	}
	env := os.Environ()
	if err := syscall.Exec(args[2], args[2:], env); err != nil {
		fmt.Fprintf(os.Stderr, "gork: exec after seccomp: %v\n", err)
		os.Exit(1)
	}
	return true
}

func wrapBubblewrapWithSeccomp(bwrapArgs []string, restrictNetwork bool) []string {
	if len(bwrapArgs) == 0 {
		return bwrapArgs
	}
	self, err := os.Executable()
	if err != nil {
		return bwrapArgs
	}
	marker := SeccompNamespaceMarker
	if restrictNetwork {
		marker = SeccompNamespaceNetMarker
	}
	command := bwrapArgs[len(bwrapArgs)-1]
	prefix := append([]string(nil), bwrapArgs[:len(bwrapArgs)-1]...)
	return append(prefix, self, marker, command)
}

// evaluateSeccompFilter is a classic-BPF interpreter for tests (nr-only filters).
func evaluateSeccompFilter(filter []unix.SockFilter, nr uint32) uint32 {
	return evaluateNamespaceLockdownFilter(filter, 0, nr, 0)
}

// evaluateNamespaceLockdownFilter is a classic-BPF interpreter for tests.
func evaluateNamespaceLockdownFilter(filter []unix.SockFilter, arch, nr, arg0 uint32) uint32 {
	pc := 0
	var a uint32
	ld := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jeq := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	jset := uint16(unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K)
	ret := uint16(unix.BPF_RET | unix.BPF_K)
	for step := 0; step < len(filter)*2; step++ {
		if pc >= len(filter) {
			panic("pc out of range")
		}
		insn := filter[pc]
		switch insn.Code {
		case ld:
			switch insn.K {
			case seccompOffNR:
				a = nr
			case seccompOffArch:
				a = arch
			case seccompOffArg0:
				a = arg0
			default:
				a = 0
			}
			pc++
		case jeq:
			if a == insn.K {
				pc = pc + 1 + int(insn.Jt)
			} else {
				pc = pc + 1 + int(insn.Jf)
			}
		case jset:
			if a&insn.K != 0 {
				pc = pc + 1 + int(insn.Jt)
			} else {
				pc = pc + 1 + int(insn.Jf)
			}
		case ret:
			return insn.K
		default:
			panic(fmt.Sprintf("unsupported opcode %#x at %d", insn.Code, pc))
		}
	}
	panic("filter did not RET")
}
