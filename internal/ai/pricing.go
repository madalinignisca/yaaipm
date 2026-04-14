package ai

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
// model the handler asks about is absent from this table, computeCostMicros
// returns 0 — the round still records successfully, just without cost data
// (safer than failing the round on a pricing lookup miss).
var pricingTable = map[string]pricingRate{
	"claude-sonnet-4-6": {inputMicrosPer1k: 3000, outputMicrosPer1k: 15000},
	"claude-opus-4-6":   {inputMicrosPer1k: 15000, outputMicrosPer1k: 75000},
	"gpt-5-mini":        {inputMicrosPer1k: 500, outputMicrosPer1k: 2000},
	"gpt-5":             {inputMicrosPer1k: 3000, outputMicrosPer1k: 15000},
	"gemini-2.5-flash":  {inputMicrosPer1k: 350, outputMicrosPer1k: 2800},
	"gemini-2.5-pro":    {inputMicrosPer1k: 2500, outputMicrosPer1k: 15000},
}

// computeCostMicros returns the cost in micros (millionths of USD) for the
// given input/output token pair against the named model. Unknown models
// return 0 — callers still record the round, they just can't price it.
func computeCostMicros(model string, inputTokens, outputTokens int) int64 {
	rate, ok := pricingTable[model]
	if !ok {
		return 0
	}
	return rate.inputMicrosPer1k*int64(inputTokens)/1000 +
		rate.outputMicrosPer1k*int64(outputTokens)/1000
}

// costCentsDelta returns the number of cents (i.e. amount_cents units in
// project_costs) to add after a round of AI cost accrual. It computes the
// delta of floor(totalMicros / 10000) before and after adding this round's
// cost, so rounding error is bounded to <1 cent per debate rather than
// <1 cent per round (spec §6).
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
func costCentsDelta(oldTotalMicros, addedMicros int64) int64 {
	newTotal := oldTotalMicros + addedMicros
	return newTotal/10000 - oldTotalMicros/10000
}
