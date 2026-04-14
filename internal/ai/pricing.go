package ai

// Model identifier constants. Centralized here so pricing, adapters, and
// handler wiring all reference the same strings — typos become compile
// errors instead of silent cost-lookup misses.
const (
	ModelClaudeSonnet46 = "claude-sonnet-4-6"
	ModelClaudeOpus46   = "claude-opus-4-6"
	ModelGPT5Mini       = "gpt-5-mini"
	ModelGPT5           = "gpt-5"
	ModelGeminiFlash    = "gemini-2.5-flash"
	ModelGeminiPro      = "gemini-2.5-pro"
)

// pricingRate expresses a per-1k-token rate in micros (millionths of USD).
type pricingRate struct {
	inputMicrosPer1k  int64
	outputMicrosPer1k int64
}

// pricingTable is the single source of truth for debate-mode AI rates.
// Update when vendors change prices; git history preserves the audit trail
// of "we were paying $X per 1k tokens during period Y".
//
// Rates are the public list prices as of the spec date (2026-04-14). If a
// model the handler asks about is absent from this table, ComputeCostMicros
// returns 0 — the round still records successfully, just without cost data
// (safer than failing the round on a pricing lookup miss).
var pricingTable = map[string]pricingRate{
	ModelClaudeSonnet46: {inputMicrosPer1k: 3000, outputMicrosPer1k: 15000},
	ModelClaudeOpus46:   {inputMicrosPer1k: 15000, outputMicrosPer1k: 75000},
	ModelGPT5Mini:       {inputMicrosPer1k: 500, outputMicrosPer1k: 2000},
	ModelGPT5:           {inputMicrosPer1k: 3000, outputMicrosPer1k: 15000},
	ModelGeminiFlash:    {inputMicrosPer1k: 350, outputMicrosPer1k: 2800},
	ModelGeminiPro:      {inputMicrosPer1k: 2500, outputMicrosPer1k: 15000},
}

// ComputeCostMicros returns the cost in micros (millionths of USD) for the
// given input/output token pair against the named model. Unknown models
// return 0 — callers still record the round, they just can't price it.
//
// Implementation sums the input+output products BEFORE dividing by 1000
// (rather than dividing each separately), which is equivalent for exact
// arithmetic but avoids dropping fractional micros on each leg when the
// token counts are small. The difference is at most 1 micro per call, but
// it compounds across many rounds if not handled this way.
//
// Exported for cross-package consumption: the debate handler in
// internal/handlers uses this to compute refiner / scorer costs before
// handing them off to CostCentsDelta.
func ComputeCostMicros(model string, inputTokens, outputTokens int) int64 {
	rate, ok := pricingTable[model]
	if !ok {
		return 0
	}
	return (rate.inputMicrosPer1k*int64(inputTokens) +
		rate.outputMicrosPer1k*int64(outputTokens)) / 1000
}

// CostCentsDelta returns the number of cents (i.e. amount_cents units in
// project_costs) to add after a round of AI cost accrual. It computes the
// delta of floor(totalMicros / 10000) before and after adding this round's
// cost, so rounding error is bounded to <1 cent PER DEBATE rather than
// <1 cent per round. This is the §6 "cumulative floor" design.
//
// Note: spec §7.3 contains stale test examples (TestCostMicrosToAddCents
// with per-round ceiling semantics) that pre-date the §6 decision to
// switch to cumulative-floor. The authoritative behavior is this
// function's implementation; §7.3 test names reference an earlier
// revision's helper that was renamed. Our actual test (pricing_test.go:
// TestCostCentsDelta + TestCostCentsDelta_AccumulatesToBoundedError)
// pins the correct cumulative-floor semantics.
//
// Arguments:
//   - oldTotalMicros: feature_debates.total_cost_micros BEFORE this round
//   - addedMicros:    the round's cost_micros (refiner or scorer)
//
// Returns the integer cents to increment project_costs by. Typically 0 or
// 1 per round; occasionally 2+ for large models or long outputs.
//
// Implementation uses integer truncation (floor for non-negative values).
// Caller is responsible for updating feature_debates.total_cost_micros to
// oldTotalMicros + addedMicros under the debate row's lock.
//
// Exported for cross-package consumption by the debate handler.
func CostCentsDelta(oldTotalMicros, addedMicros int64) int64 {
	newTotal := oldTotalMicros + addedMicros
	return newTotal/10000 - oldTotalMicros/10000
}
