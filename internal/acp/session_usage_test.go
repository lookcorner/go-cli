package acp

import (
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
)

func TestSessionUsageLedgerAggregatesAndScrubsPartialCosts(t *testing.T) {
	var ledger sessionUsageLedger
	cost := int64(20_000_000)
	ledger.recordTurn(agent.Result{Model: "grok-build", UsageHistory: []agent.ModelUsage{
		{Model: "grok-build", Usage: api.Usage{InputTokens: 100, OutputTokens: 10, CostUSDTicks: &cost}},
	}})
	wire := ledger.wire()
	if wire["inputTokens"] != uint64(100) || wire["outputTokens"] != uint64(10) || wire["numTurns"] != uint64(1) || wire["costUsdTicks"] != int64(20_000_000) {
		t.Fatalf("wire=%#v", wire)
	}
	models := wire["modelUsage"].(map[string]any)
	row := models["grok-build"].(map[string]any)
	if row["inputTokens"] != uint64(100) || row["costUsdTicks"] != int64(20_000_000) {
		t.Fatalf("model=%#v", row)
	}

	ledger.recordTurn(agent.Result{Model: "grok-build", Usage: &api.Usage{InputTokens: 50, OutputTokens: 5}})
	wire = ledger.wire()
	if wire["inputTokens"] != uint64(150) || wire["numTurns"] != uint64(2) || wire["costUsdTicks"] != nil || wire["costIsPartial"] != true {
		t.Fatalf("partial wire=%#v", wire)
	}
	row = wire["modelUsage"].(map[string]any)["grok-build"].(map[string]any)
	if row["costUsdTicks"] != nil || row["costIsPartial"] != true {
		t.Fatalf("partial model=%#v", row)
	}
}

func TestSessionUsageLedgerEmptyWire(t *testing.T) {
	var ledger *sessionUsageLedger
	wire := ledger.wire()
	if wire["numTurns"] != uint64(0) || wire["inputTokens"] != uint64(0) {
		t.Fatalf("empty=%#v", wire)
	}
}
