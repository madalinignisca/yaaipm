package config

import "testing"

// TestAuthRateLimitDefaults pins the production defaults. These bounds are a
// security control, and the only reason they became configurable was to let the
// E2E suite drive several auth requests per user without being throttled
// (#136). If an env var is absent, malformed, or non-positive, the strict
// default must apply — "0" must never be a way to switch rate limiting off.
func TestAuthRateLimitDefaults(t *testing.T) {
	if got := authRateLimitRPS(); got != 0.5 {
		t.Errorf("unset RPS = %v, want the 0.5 default", got)
	}
	if got := authRateLimitBurst(); got != 5 {
		t.Errorf("unset burst = %v, want the 5 default", got)
	}

	rpsCases := []struct{ name, value string }{
		{"zero does not disable the limiter", "0"},
		{"negative does not disable the limiter", "-1"},
		{"garbage does not disable the limiter", "unlimited"},
		{"empty does not disable the limiter", ""},
	}
	for _, tc := range rpsCases {
		t.Run("rps: "+tc.name, func(t *testing.T) {
			t.Setenv("AUTH_RATE_LIMIT_RPS", tc.value)
			if got := authRateLimitRPS(); got != 0.5 {
				t.Errorf("value %q gave %v, want the 0.5 default", tc.value, got)
			}
		})
	}
	for _, tc := range rpsCases {
		t.Run("burst: "+tc.name, func(t *testing.T) {
			t.Setenv("AUTH_RATE_LIMIT_BURST", tc.value)
			if got := authRateLimitBurst(); got != 5 {
				t.Errorf("value %q gave %v, want the 5 default", tc.value, got)
			}
		})
	}

	// A legitimate override still applies, or the E2E stack cannot relax it.
	t.Setenv("AUTH_RATE_LIMIT_RPS", "100")
	if got := authRateLimitRPS(); got != 100 {
		t.Errorf("valid override gave %v, want 100", got)
	}
	t.Setenv("AUTH_RATE_LIMIT_BURST", "200")
	if got := authRateLimitBurst(); got != 200 {
		t.Errorf("valid override gave %v, want 200", got)
	}
}
