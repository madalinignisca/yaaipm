package ai

import "testing"

func TestComputeCostMicros_KnownModels(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		inputTok int
		outTok   int
		want     int64
	}{
		{"gemini flash 1k/1k", ModelGeminiFlash, 1000, 1000, 350 + 2800},
		{"claude sonnet 2k in / 500 out", ModelClaudeSonnet46, 2000, 500, 6000 + 7500},
		{"gpt-5-mini 500 in / 200 out", ModelGPT5Mini, 500, 200, 250 + 400},
		{"zero tokens", ModelGeminiFlash, 0, 0, 0},
	}
	for _, c := range cases {
		got := ComputeCostMicros(c.model, c.inputTok, c.outTok)
		if got != c.want {
			t.Errorf("%s: ComputeCostMicros(%q, %d, %d) = %d, want %d",
				c.name, c.model, c.inputTok, c.outTok, got, c.want)
		}
	}
}

func TestComputeCostMicros_UnknownModelReturnsZero(t *testing.T) {
	if got := ComputeCostMicros("not-a-real-model", 1000, 1000); got != 0 {
		t.Errorf("unknown model should return 0, got %d", got)
	}
}

// TestComputeCostMicros_SumBeforeDividePreservesPrecision verifies the
// sum-then-divide ordering. With 1 input token and 1 output token at
// 500+2000 micros/1k, naive (500*1/1000)+(2000*1/1000) = 0+2 = 2, while
// the correct (500+2000)/1000 = 2500/1000 = 2. The difference shows up
// at sub-1k token counts; the sum-first form is strictly ≥ per-leg form.
func TestComputeCostMicros_SumBeforeDividePreservesPrecision(t *testing.T) {
	// 1 input @ 500 micros/1k, 1 output @ 2000 micros/1k.
	// Per-leg naive:   500*1/1000 + 2000*1/1000 = 0 + 2 = 2
	// Sum-first:       (500*1 + 2000*1) / 1000 = 2500/1000 = 2
	// Both agree on 2 here. A tighter case:
	// 1 in @ 350, 1 out @ 350.  Sum: 700/1000 = 0.  Per-leg: 0+0 = 0. Both 0.
	// A case where they DIFFER: 999 in @ 350, 1 out @ 2800.
	//   Per-leg: 999*350/1000 + 1*2800/1000 = 349 + 2 = 351.
	//   Sum:    (999*350 + 1*2800)/1000    = (349650 + 2800)/1000 = 352.
	got := ComputeCostMicros(ModelGeminiFlash, 999, 1)
	want := int64(352)
	if got != want {
		t.Errorf("sum-then-divide ordering lost precision: got %d, want %d", got, want)
	}
}

func TestCostCentsDelta(t *testing.T) {
	cases := []struct {
		name       string
		old, added int64
		want       int64
	}{
		{"0 → 0c under 1c", 0, 9999, 0},
		{"0 → 1c exactly", 0, 10000, 1},
		{"0 → 1c one over", 0, 10001, 1},
		{"crosses 0→1 boundary", 9000, 1000, 1},
		{"already at 1c, stays 1c", 10000, 100, 0},
		{"1c → 1c just under 2c", 10000, 9999, 0},
		{"1c → 2c exactly", 10000, 10000, 1},
		{"big jump across multiple cents", 0, 1234567, 123},
	}
	for _, c := range cases {
		got := CostCentsDelta(c.old, c.added)
		if got != c.want {
			t.Errorf("%s: CostCentsDelta(%d, %d) = %d, want %d",
				c.name, c.old, c.added, got, c.want)
		}
	}
}

// TestCostCentsDelta_AccumulatesToBoundedError is the core property of the
// cumulative-floor conversion: across N rounds with small per-round costs,
// total error vs the ideal cent value is strictly less than 1 cent.
// Contrast with a naive per-round ceiling which would over-report by ~N
// cents in the same scenario — see spec §6.
func TestCostCentsDelta_AccumulatesToBoundedError(t *testing.T) {
	total := int64(0) // accumulated project_costs cents
	totalMicros := int64(0)

	// 100 rounds at 100 micros each. Ideal total: 10_000 micros = 1 cent.
	for range 100 {
		delta := CostCentsDelta(totalMicros, 100)
		total += delta
		totalMicros += 100
	}

	// totalMicros = 10_000 exactly, cents should be 1.
	if total != 1 {
		t.Errorf("expected 1 cent accumulated after 100 rounds of 100 micros, got %d", total)
	}
	// Property: no matter where we stop, |total - totalMicros/10000| < 1
	// (guaranteed by the delta formula: each delta = floor(new) - floor(old)).
}
