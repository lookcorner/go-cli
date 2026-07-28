//go:build !linux

package tools

func tryShellCgroup(CgroupMemoryConfig) shellCgroup { return nil }
