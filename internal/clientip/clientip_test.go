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

// Header.Get returns only the FIRST value. The security premise of this
// package is that Cloudflare's value cannot be forged — but Cloudflare's
// public documentation does not actually state whether a client-supplied
// CF-Connecting-IP is overwritten or appended to. If it were ever appended,
// the first value would be the caller's and the bypass would be back. Taking
// the last value is correct under either behavior.
func TestFromUsesTheLastCloudflareConnectingIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	r.RemoteAddr = "10.42.1.5:41234"
	r.Header.Add("Cf-Connecting-Ip", "203.0.113.9")  // as a caller might forge it
	r.Header.Add("Cf-Connecting-Ip", "198.51.100.7") // as the edge appends it

	if got := clientip.From(r); got != "198.51.100.7" {
		t.Errorf("From() = %q, want the last value %q — a forged first value must not win",
			got, "198.51.100.7")
	}
}

// One address must produce one rate-limit bucket however it is spelled.
// Without the net.IP round-trip, two spellings of the same client become
// two keys and the limiter is partially bypassable.
func TestFromCanonicalizesEquivalentSpellings(t *testing.T) {
	key := func(v string) string {
		r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
		r.RemoteAddr = "10.42.1.5:41234"
		r.Header.Set("Cf-Connecting-Ip", v)
		return clientip.From(r)
	}

	for _, tc := range []struct{ name, a, b string }{
		{"ipv4-mapped ipv6", "203.0.113.9", "::ffff:203.0.113.9"},
		{"ipv4-mapped uppercase", "203.0.113.9", "::FFFF:203.0.113.9"},
		{"expanded ipv6", "2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001"},
		{"ipv6 case", "2001:db8::1", "2001:DB8::1"},
	} {
		if got, want := key(tc.b), key(tc.a); got != want {
			t.Errorf("%s: From(%q) = %q, want %q — same client, two buckets",
				tc.name, tc.b, got, want)
		}
	}
}
