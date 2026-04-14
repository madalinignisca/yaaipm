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
		{"gemini flash 1k/1k", "gemini-2.5-flash", 1000, 1000, 350 + 2800},
		{"claude sonnet 2k in / 500 out", "claude-sonnet-4-6", 2000, 500, 6000 + 7500},
		{"gpt-5-mini 500 in / 200 out", "gpt-5-mini", 500, 200, 250 + 400},
		{"zero tokens", "gemini-2.5-flash", 0, 0, 0},
	}
	for _, c := range cases {
		got := computeCostMicros(c.model, c.inputTok, c.outTok)
		if got != c.want {
			t.Errorf("%s: computeCostMicros(%q, %d, %d) = %d, want %d",
				c.name, c.model, c.inputTok, c.outTok, got, c.want)
		}
	}
}

func TestComputeCostMicros_UnknownModelReturnsZero(t *testing.T) {
	if got := computeCostMicros("not-a-real-model", 1000, 1000); got != 0 {
		t.Errorf("unknown model should return 0, got %d", got)
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
		got := costCentsDelta(c.old, c.added)
		if got != c.want {
			t.Errorf("%s: costCentsDelta(%d, %d) = %d, want %d",
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
		delta := costCentsDelta(totalMicros, 100)
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
