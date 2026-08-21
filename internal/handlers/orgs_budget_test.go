package handlers

import (
	"errors"
	"testing"
)

// TestParseUSDCents covers the accept/reject grammar from the plan's
// test matrix. parseUSDCents deliberately does NOT reuse costs.go's
// parseToCents: that helper uses ParseFloat + math.Round, which silently
// accepts scientific notation / NaN / Inf and rounds instead of
// rejecting extra decimal places — wrong for a money control per spec §6.
func TestParseUSDCents(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr error
	}{
		{"zero", "0", 0, nil},
		{"whole dollars", "25", 2500, nil},
		{"one decimal", "25.5", 2550, nil},
		{"two decimals", "25.50", 2550, nil},
		{"leading dollar sign", "$25.00", 2500, nil},
		{"surrounding whitespace", " 25.00 ", 2500, nil},
		{"at the max bound", "1000000", 100_000_000, nil},

		{"empty string", "", 0, errBudgetNotANumber},
		{"negative", "-1", 0, errBudgetNotANumber},
		{"three decimals rejected not rounded", "25.555", 0, errBudgetTooManyDecimals},
		{"scientific notation rejected", "1e3", 0, errBudgetNotANumber},
		{"non-numeric", "abc", 0, errBudgetNotANumber},
		{"thousands separator rejected", "1,000", 0, errBudgetNotANumber},
		{"NaN literal rejected", "NaN", 0, errBudgetNotANumber},
		{"Inf literal rejected", "Inf", 0, errBudgetNotANumber},
		{"over the max bound", "1000000.01", 0, errBudgetOutOfRange},
		{"trailing dot with no digits", "25.", 0, errBudgetNotANumber},
		{"leading dot with no whole part", ".50", 0, errBudgetNotANumber},
		{"leading plus rejected", "+25", 0, errBudgetNotANumber},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUSDCents(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("parseUSDCents(%q) err = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUSDCents(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseUSDCents(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatUSDCents(t *testing.T) {
	tests := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{2500, "25.00"},
		{2550, "25.50"},
		{100_000_000, "1000000.00"},
		// Negative input should never happen (callers only ever format a
		// validated non-negative cap), but %02d of a negative remainder
		// renders "0.-5" rather than "-0.05" if unguarded — this is a
		// bug-catcher, not an expected code path.
		{-5, "-0.05"},
	}
	for _, tc := range tests {
		got := formatUSDCents(tc.cents)
		if got != tc.want {
			t.Errorf("formatUSDCents(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}
