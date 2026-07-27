package acp

import (
	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
)

// sessionUsageLedger accumulates main-loop model spend for x.ai/session/usage.
// Costs scrub when any call lacked ticks (absence ≠ free), matching Rust PromptUsage.
type sessionUsageLedger struct {
	inputTokens      uint64
	outputTokens     uint64
	cachedReadTokens uint64
	reasoningTokens  uint64
	modelCalls       uint64
	costUsdTicks     int64
	costCalls        uint64
	costMissingCalls uint64
	numTurns         uint64
	byModel          map[string]*sessionUsageModel
}

type sessionUsageModel struct {
	inputTokens      uint64
	outputTokens     uint64
	cachedReadTokens uint64
	reasoningTokens  uint64
	modelCalls       uint64
	costUsdTicks     int64
	costCalls        uint64
	costMissingCalls uint64
}

func (l *sessionUsageLedger) recordTurn(result agent.Result) {
	if l == nil {
		return
	}
	history := result.UsageHistory
	if len(history) == 0 && result.Usage != nil {
		history = []agent.ModelUsage{{Model: result.Model, Usage: *result.Usage}}
	}
	if len(history) == 0 {
		return
	}
	l.numTurns++
	for _, record := range history {
		l.add(record.Model, record.Usage)
	}
}

func (l *sessionUsageLedger) add(model string, usage api.Usage) {
	l.inputTokens += uint64(usage.InputTokens)
	l.outputTokens += uint64(usage.OutputTokens)
	l.cachedReadTokens += uint64(usage.CachedReadTokens)
	l.reasoningTokens += uint64(usage.ReasoningTokens)
	l.modelCalls++
	if usage.CostUSDTicks != nil {
		l.costUsdTicks += *usage.CostUSDTicks
		l.costCalls++
	} else {
		l.costMissingCalls++
	}
	if model == "" {
		return
	}
	if l.byModel == nil {
		l.byModel = make(map[string]*sessionUsageModel)
	}
	entry := l.byModel[model]
	if entry == nil {
		entry = &sessionUsageModel{}
		l.byModel[model] = entry
	}
	entry.inputTokens += uint64(usage.InputTokens)
	entry.outputTokens += uint64(usage.OutputTokens)
	entry.cachedReadTokens += uint64(usage.CachedReadTokens)
	entry.reasoningTokens += uint64(usage.ReasoningTokens)
	entry.modelCalls++
	if usage.CostUSDTicks != nil {
		entry.costUsdTicks += *usage.CostUSDTicks
		entry.costCalls++
	} else {
		entry.costMissingCalls++
	}
}

func (l *sessionUsageLedger) wire() map[string]any {
	if l == nil {
		return emptySessionUsageWire()
	}
	partial := l.costMissingCalls > 0 && l.costCalls > 0
	// All calls missing cost → partial with no ticks (scrub).
	// Mix of present/missing → partial + scrub ticks.
	// All present → include ticks.
	scrub := l.costMissingCalls > 0
	usage := map[string]any{
		"inputTokens":      l.inputTokens,
		"outputTokens":     l.outputTokens,
		"totalTokens":      l.inputTokens + l.outputTokens,
		"cachedReadTokens": l.cachedReadTokens,
		"reasoningTokens":  l.reasoningTokens,
		"modelCalls":       l.modelCalls,
		"numTurns":         l.numTurns,
	}
	if !scrub && l.costCalls > 0 {
		usage["costUsdTicks"] = l.costUsdTicks
	}
	if scrub || partial {
		usage["costIsPartial"] = true
	}
	models := map[string]any{}
	for name, entry := range l.byModel {
		row := map[string]any{
			"inputTokens":      entry.inputTokens,
			"outputTokens":     entry.outputTokens,
			"totalTokens":      entry.inputTokens + entry.outputTokens,
			"cachedReadTokens": entry.cachedReadTokens,
			"reasoningTokens":  entry.reasoningTokens,
			"modelCalls":       entry.modelCalls,
		}
		if !scrub && entry.costCalls > 0 {
			row["costUsdTicks"] = entry.costUsdTicks
		}
		if scrub {
			row["costIsPartial"] = true
		}
		models[name] = row
	}
	if len(models) > 0 {
		usage["modelUsage"] = models
	}
	return usage
}

func emptySessionUsageWire() map[string]any {
	return map[string]any{
		"inputTokens":      uint64(0),
		"outputTokens":     uint64(0),
		"totalTokens":      uint64(0),
		"cachedReadTokens": uint64(0),
		"reasoningTokens":  uint64(0),
		"modelCalls":       uint64(0),
		"numTurns":         uint64(0),
	}
}
