package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madalin/forgedesk/internal/clientip"
)

// The bug this package exists to fix: the rate limiter used the raw
// X-Forwarded-For header as its per-visitor key, so a caller who varied
// the header on every request got a fresh limiter every time and was
// never rate limited at all.
func TestFromIgnoresClientSuppliedXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	r.RemoteAddr = "10.42.1.5:41234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := clientip.From(r); got != "10.42.1.5" {
		t.Errorf("From() = %q, want %q — X-Forwarded-For must not be trusted", got, "10.42.1.5")
	}
}

// The regression test for the bypass itself, stated as the property that
// actually matters: the same peer must map to the same key no matter what
// headers it sends. If this fails, the rate limiter can be evaded.
func TestFromIsStableAcrossSpoofedHeaders(t *testing.T) {
	key := func(xff string) string {
		r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
		r.RemoteAddr = "10.42.1.5:41234"
		r.Header.Set("X-Forwarded-For", xff)
		return clientip.From(r)
	}

	first, second := key("1.1.1.1"), key("2.2.2.2, 3.3.3.3")
	if first != second {
		t.Errorf("From() varied with X-Forwarded-For: %q vs %q — limiter is bypassable", first, second)
	}
	// Stability alone is satisfied by any constant, including "", so pin
	// the value too: the key must be the real peer, not just consistent.
	if first != "10.42.1.5" {
		t.Errorf("From() = %q, want the peer address %q", first, "10.42.1.5")
	}
}

func TestFromPrefersCloudflareConnectingIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	r.RemoteAddr = "10.42.1.5:41234"
	r.Header.Set("Cf-Connecting-Ip", "198.51.100.7")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")

	if got := clientip.From(r); got != "198.51.100.7" {
		t.Errorf("From() = %q, want the CF-Connecting-IP value", got)
	}
}

func TestFromRejectsMalformedCloudflareConnectingIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	r.RemoteAddr = "10.42.1.5:41234"
	r.Header.Set("Cf-Connecting-Ip", "not-an-ip")

	if got := clientip.From(r); got != "10.42.1.5" {
		t.Errorf("From() = %q, want fallback to RemoteAddr for an unparseable header", got)
	}
}

func TestFromStripsPortFromRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.RemoteAddr = "192.0.2.4:55555"

	if got := clientip.From(r); got != "192.0.2.4" {
		t.Errorf("From() = %q, want %q", got, "192.0.2.4")
	}
}

func TestFromHandlesIPv6RemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.RemoteAddr = "[2001:db8::1]:44321"

	if got := clientip.From(r); got != "2001:db8::1" {
		t.Errorf("From() = %q, want %q", got, "2001:db8::1")
	}
}
