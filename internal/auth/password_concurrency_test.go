package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHashingConcurrencyIsBounded pins the fix for #142.
//
// Argon2id deliberately uses 64 MB of working memory per hash. That protects
// the password, but it also means memory use scales with the number of
// concurrent hashes — and nothing bounded that number. The auth rate limiter
// is PER IP with a burst of 5, so an attacker using several source addresses
// gets several bursts and can drive concurrency arbitrarily high. Raising the
// container memory limit therefore does not fix this; only bounding the
// hashing itself does.
//
// A pod was OOMKilled in production this way, from unauthenticated requests to
// a public endpoint with no valid account.
func TestHashingConcurrencyIsBounded(t *testing.T) {
	const limit = 2
	restore := setHashConcurrencyForTest(t, limit)
	defer restore()

	var inFlight, peak int64
	var wg sync.WaitGroup

	for range 12 {
		wg.Go(func() {
			// Generous budget: this test is about the ceiling, not latency.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			done, err := acquireHashSlot(ctx)
			if err != nil {
				return
			}
			defer done()

			cur := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
		})
	}
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > limit {
		t.Errorf("peak concurrent hashes = %d, want <= %d — memory is unbounded and the pod can be OOM-killed", got, limit)
	}
}

// TestHashingShedsLoadRatherThanQueueingForever is the other half of #142.
//
// A bound that makes callers queue indefinitely converts a crash into a hang,
// which is still a denial of service. Once the ceiling is saturated, further
// work must be refused promptly with a distinct error so the handler can shed
// load rather than hold the request open.
func TestHashingShedsLoadRatherThanQueueingForever(t *testing.T) {
	restore := setHashConcurrencyForTest(t, 1)
	defer restore()

	done, err := acquireHashSlot(context.Background())
	if err != nil {
		t.Fatalf("first acquire should succeed: %v", err)
	}
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := acquireHashSlot(ctx); !errors.Is(err, ErrHashingBusy) {
		t.Errorf("second acquire = %v, want ErrHashingBusy", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %v before shedding; a saturated hasher must refuse promptly", elapsed)
	}
}

// TestHashGateNeverReturnsNil guards a hazard that hangs rather than fails.
//
// A send on a nil channel blocks forever. If the gate were ever nil — because
// nothing had configured it, or because something set it back to a zero value
// — every password check would block permanently, which is a worse outage than
// the OOM this file prevents. It is also invisible: no error, no panic, just a
// server that stops answering.
//
// It is not hypothetical. An early version of the test helper restored the
// gate to its previous nil value and hung the suite for 6m40s.
func TestHashGateNeverReturnsNil(t *testing.T) {
	hashMu.Lock()
	hashSlots = nil // simulate "never configured"
	hashMu.Unlock()

	if g := hashGate(); g == nil {
		t.Fatal("hashGate returned a nil channel; every hash would block forever")
	}
	if cap(hashGate()) == 0 {
		t.Error("gate has zero capacity; acquiring a slot could never succeed")
	}

	// And it must actually be usable without any explicit configuration.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := HashPassword(context.Background(), "unconfigured-gate"); err != nil {
			t.Errorf("hashing with a default gate failed: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("hashing blocked with an unconfigured gate")
	}
}

// TestSetHashConcurrencyRejectsNonPositive: the bound must always exist. A
// zero or negative value from a misconfiguration must be ignored, not applied
// as a gate nothing can pass through.
func TestSetHashConcurrencyRejectsNonPositive(t *testing.T) {
	restore := setHashConcurrencyForTest(t, 3)
	defer restore()

	for _, n := range []int{0, -1} {
		SetHashConcurrency(n)
		if got := cap(hashGate()); got != 3 {
			t.Errorf("SetHashConcurrency(%d) changed capacity to %d, want it left at 3", n, got)
		}
	}
}

// TestPasswordFunctionsGoThroughTheGate closes a hole found by mutation
// testing: the concurrency tests above exercise acquireHashSlot directly, so
// removing the gate from HashPassword or VerifyPassword left them all green.
// The bound is worthless if the functions that spend the memory bypass it.
func TestPasswordFunctionsGoThroughTheGate(t *testing.T) {
	restore := setHashConcurrencyForTest(t, 1)
	defer restore()

	// Take the only slot and keep it.
	release, err := acquireHashSlot(context.Background())
	if err != nil {
		t.Fatalf("acquiring the only slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := HashPassword(ctx, "whatever"); !errors.Is(err, ErrHashingBusy) {
		t.Errorf("HashPassword with a saturated gate = %v, want ErrHashingBusy (it is not gated)", err)
	}

	// A valid hash to verify against, produced while we still hold the slot —
	// so it must come from outside the gate.
	release()
	encoded, hashErr := HashPassword(context.Background(), "correct horse")
	if hashErr != nil {
		t.Fatalf("producing a hash: %v", hashErr)
	}
	release2, acqErr := acquireHashSlot(context.Background())
	if acqErr != nil {
		t.Fatalf("re-acquiring: %v", acqErr)
	}
	defer release2()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := VerifyPassword(ctx2, "correct horse", encoded); !errors.Is(err, ErrHashingBusy) {
		t.Errorf("VerifyPassword with a saturated gate = %v, want ErrHashingBusy (it is not gated)", err)
	}
}

// TestVerifyRecoveryCodeDoesNotRejectValidCodeWhenBusy pins a defect the
// hashing gate introduced.
//
// VerifyRecoveryCode used to discard the error from VerifyPassword, which was
// harmless while that call could not fail. Once a saturated gate could return
// ErrHashingBusy, a discarded error read as "not a match" — so a VALID
// recovery code would be reported invalid. That is the path a user reaches
// after losing their authenticator, and the codes are single-use, so a false
// rejection can cost them several of them.
func TestVerifyRecoveryCodeDoesNotRejectValidCodeWhenBusy(t *testing.T) {
	restore := setHashConcurrencyForTest(t, 1)
	defer restore()

	valid := "TESTCODE1"
	hashed, err := HashPassword(context.Background(), valid)
	if err != nil {
		t.Fatalf("hashing recovery code: %v", err)
	}
	codes := []string{hashed}

	// Sanity: it matches when the gate is free.
	if idx, vErr := VerifyRecoveryCode(context.Background(), valid, codes); idx != 0 || vErr != nil {
		t.Fatalf("unsaturated: idx=%d err=%v, want 0 / nil", idx, vErr)
	}

	// Saturate, then check the same valid code.
	release, err := acquireHashSlot(context.Background())
	if err != nil {
		t.Fatalf("acquiring the only slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	idx, vErr := VerifyRecoveryCode(ctx, valid, codes)
	if !errors.Is(vErr, ErrHashingBusy) {
		t.Errorf("saturated verify returned err=%v, want ErrHashingBusy", vErr)
	}
	if idx >= 0 {
		t.Errorf("saturated verify returned idx=%d; must not claim a match it could not check", idx)
	}
	// The crucial part: the caller must be able to tell "busy" from "invalid".
	if vErr == nil && idx == -1 {
		t.Error("a valid recovery code was silently reported invalid because the server was busy")
	}
}
