package auth

import "testing"

// setHashConcurrencyForTest rebuilds the gate with a known ceiling and puts a
// WORKING gate back afterwards.
//
// It deliberately does not restore the previous value verbatim: that value can
// be nil (when nothing has hashed yet), and handing a nil channel back to
// hashGate would block every later test forever. Learned the hard way — this
// hung the suite for 6m40s.
func setHashConcurrencyForTest(t *testing.T, n int) (restore func()) {
	t.Helper()
	hashMu.RLock()
	prev := hashSlots
	hashMu.RUnlock()

	SetHashConcurrency(n)
	return func() {
		if prev == nil {
			SetHashConcurrency(defaultHashConcurrency)
			return
		}
		hashMu.Lock()
		hashSlots = prev
		hashMu.Unlock()
	}
}
