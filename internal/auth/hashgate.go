package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrHashingBusy is returned when every hashing slot is occupied and the
// caller's context expired before one came free. Handlers should shed the
// request (503/429) rather than hold it open — see acquireHashSlot.
var ErrHashingBusy = errors.New("password hashing at capacity")

const (
	// One Argon2id call holds argonMemory (64 MiB) live, so the memory bound is
	// that times this ceiling.
	//
	// defaultHashConcurrency caps concurrent Argon2id work at 2 × 64 MB =
	// 128 MB. The server container is limited to 256Mi and idles around
	// 80 MiB, so this leaves headroom instead of racing the OOM killer.
	//
	// Raise it only together with the container memory limit: the product of
	// this and argonMemoryBytes is the floor the pod must be able to hold.
	defaultHashConcurrency = 2
)

// maxHashWait bounds how long a caller waits for a slot.
//
// This is load-bearing, not a nicety. Production request contexts carry NO
// deadline: http.Server's ReadTimeout and WriteTimeout are CONNECTION
// deadlines and do not cancel r.Context(), and there is no timeout middleware
// in the chain. A request context ends only when the client disconnects.
//
// Without an independent bound, a saturated gate would park every handler
// goroutine on the channel send until a slot freed — turning the crash this
// file prevents into an unbounded queue, which is the same denial of service
// wearing different clothes. The shed below would be unreachable in
// production and reachable only in tests that inject a deadline of their own.
//
// A package variable rather than a constant so tests can shorten it.
var maxHashWait = 2 * time.Second

// hashSlots bounds how many Argon2id computations run at once.
//
// WHY THIS EXISTS (#142): Argon2id is deliberately memory-hard — 64 MB per
// call — which protects the password but makes memory scale with concurrency.
// The auth rate limiter is PER IP with a burst of 5, so an attacker spreading
// requests over several addresses gets several bursts and can drive
// concurrency arbitrarily high. That means raising the container memory limit
// does NOT fix the problem; it only moves the threshold. A pod was OOMKilled
// in production by unauthenticated requests to a public endpoint.
//
// Login hashes even for an unknown email (the user-enumeration defense), so no
// valid account is needed to trigger the work. Gating both Argon2id entry
// points here covers every path — login, register, password change,
// invitations, and recovery codes, whose verify loop runs up to ten hashes.
//
// A plain buffered channel rather than golang.org/x/sync/semaphore: this needs
// one token per caller and nothing weighted, so the standard library does it.
// Guarded by hashMu rather than sync.Once: hashGate must NEVER hand back a nil
// channel. A send on a nil channel blocks forever, so a nil gate would turn
// every password check into a permanent hang — a worse outage than the one
// this file prevents.
var (
	hashMu    sync.RWMutex
	hashSlots chan struct{}
)

// SetHashConcurrency sizes the gate. Call once at startup, before serving.
// A non-positive value is ignored so a misconfiguration cannot remove the
// bound — the whole point is that it is always present.
//
// Env parsing lives in internal/config with every other setting rather than
// here, so there is one place to look for what an environment variable does.
func SetHashConcurrency(n int) {
	if n <= 0 {
		return
	}
	hashMu.Lock()
	defer hashMu.Unlock()
	hashSlots = make(chan struct{}, n)
}

// hashGate returns the gate, building a default one on first use so hashing
// works even if SetHashConcurrency was never called. The bound must exist
// whether or not startup remembered to configure it.
func hashGate() chan struct{} {
	hashMu.RLock()
	g := hashSlots
	hashMu.RUnlock()
	if g != nil {
		return g
	}

	hashMu.Lock()
	defer hashMu.Unlock()
	if hashSlots == nil {
		hashSlots = make(chan struct{}, defaultHashConcurrency)
	}
	return hashSlots
}

// acquireHashSlot reserves capacity for one Argon2id computation. The returned
// function releases it and must always be called.
//
// It respects the caller's context, so a client that has gone away stops
// occupying a place in the queue. Queueing forever would turn a crash into a
// hang, which is still a denial of service — hence ErrHashingBusy.
func acquireHashSlot(ctx context.Context) (release func(), err error) {
	// Check the caller first. Beginning a 64 MB computation for a client that
	// has already disconnected is pure waste, and it is worst precisely under
	// the load this gate exists to survive. It also makes the outcome
	// deterministic: without it, a select with both a free slot and a done
	// context would choose between them at random.
	if ctx.Err() != nil {
		return nil, ErrHashingBusy
	}

	// Bound the wait ourselves as well as honoring the caller's context: see
	// maxHashWait for why the caller alone is not enough.
	waitCtx, cancel := context.WithTimeout(ctx, maxHashWait)
	defer cancel()

	gate := hashGate()
	select {
	case gate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-gate }) }, nil
	case <-waitCtx.Done():
		return nil, ErrHashingBusy
	}
}

// SaturateForTest fills every slot and returns a release. It exists because
// handler tests in other packages must be able to produce a genuinely
// saturated gate: the only alternative was canceling the request context,
// which makes the database lookup fail first and so never reaches the
// credential-verification branch that the enumeration symmetry depends on.
//
// Named and documented as test-only rather than hidden behind a build tag so
// that its single purpose is obvious at the call site.
func SaturateForTest() (release func()) {
	gate := hashGate()
	held := 0
	for {
		select {
		case gate <- struct{}{}:
			held++
		default:
			var once sync.Once
			return func() {
				once.Do(func() {
					for range held {
						<-gate
					}
				})
			}
		}
	}
}
