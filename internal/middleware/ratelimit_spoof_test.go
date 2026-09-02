package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/madalin/forgedesk/internal/middleware"
)

// The /login rate limiter is the mitigation that bounds password guessing.
// It previously keyed on the raw X-Forwarded-For header, so a caller that
// changed the header on each request was handed a brand new limiter every
// time and was never limited. This asserts the property that matters: one
// peer gets one bucket, whatever headers it sends.
func TestLimitCountsSpoofedXForwardedForAsOneVisitor(t *testing.T) {
	rl := middleware.NewRateLimiter(0.1, 2) // burst of 2, then throttled
	h := rl.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const attempts = 10
	limited := 0
	for i := range attempts {
		r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
		r.RemoteAddr = "10.42.1.5:41234"
		// A different forged value on every request — the bypass.
		r.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}

	if limited == 0 {
		t.Fatalf("all %d spoofed requests were allowed — the rate limiter is bypassable "+
			"by varying X-Forwarded-For", attempts)
	}
	if want := attempts - 2; limited < want {
		t.Errorf("limited %d of %d requests, want at least %d (burst is 2)", limited, attempts, want)
	}
}

// Distinct real clients must still get their own buckets, or the fix would
// turn the limiter into a global lock that one caller could use to lock
// everyone else out.
func TestLimitKeepsDistinctCloudflareClientsSeparate(t *testing.T) {
	rl := middleware.NewRateLimiter(0.1, 1)
	h := rl.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func(clientIP string) int {
		r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
		r.RemoteAddr = "10.42.1.5:41234"
		r.Header.Set("Cf-Connecting-Ip", clientIP)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	if code := call("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first client got %d, want 200", code)
	}
	if code := call("198.51.100.2"); code != http.StatusOK {
		t.Errorf("second, different client got %d, want 200 — buckets are not per-client", code)
	}
}
