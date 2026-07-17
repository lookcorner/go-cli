package api

import "fmt"

type PruningConfig struct {
	Enabled           bool
	KeepLastNTurns    int
	SoftTrimThreshold int
	SoftTrimHead      int
	SoftTrimTail      int
	HardClearAgeTurns int
}

func DefaultPruningConfig() PruningConfig {
	return PruningConfig{
		Enabled: true, KeepLastNTurns: 3, SoftTrimThreshold: 4000,
		SoftTrimHead: 1500, SoftTrimTail: 1500, HardClearAgeTurns: 10,
	}
}

func pruneToolResult(value string, age int, cfg PruningConfig) string {
	if !cfg.Enabled || age < cfg.KeepLastNTurns {
		return value
	}
	if age >= cfg.HardClearAgeTurns {
		return fmt.Sprintf("[old tool result cleared after %d turns]", age)
	}
	runes := []rune(value)
	if len(runes) <= cfg.SoftTrimThreshold {
		return value
	}
	head, tail := min(cfg.SoftTrimHead, len(runes)), min(cfg.SoftTrimTail, len(runes))
	if head+tail >= len(runes) {
		return value
	}
	removed := len(runes) - head - tail
	return string(runes[:head]) + fmt.Sprintf("\n[... %d characters pruned from old tool result ...]\n", removed) + string(runes[len(runes)-tail:])
}
