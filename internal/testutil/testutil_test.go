package testutil

import "testing"

// TestRequireTestDB pins the opt-in that stops CI from passing by not running.
//
// The default must stay "skip" so a developer without Postgres is not blocked.
// The opt-in must be an explicit declaration, and the obvious ways of writing
// "off" must not accidentally read as "on" — a CI file that sets
// REQUIRE_TEST_DB=false and silently got hard failures would be its own
// confusing outage.
func TestRequireTestDB(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"unset skips, so local runs without Postgres still work", "", false, false},
		{"empty is not a declaration", "", true, false},
		{"whitespace is not a declaration", "   ", true, false},
		{"0 means off", "0", true, false},
		{"false means off", "false", true, false},
		{"FALSE means off", "FALSE", true, false},
		{"1 requires the database", "1", true, true},
		{"true requires the database", "true", true, true},
		{"any other value requires the database", "yes", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("REQUIRE_TEST_DB", tt.value)
			} else {
				t.Setenv("REQUIRE_TEST_DB", "")
			}
			if got := requireTestDB(); got != tt.want {
				t.Errorf("requireTestDB() with %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
