//go:build !darwin || !cgo

package config

func readManagedRequirements() []byte { return nil }
