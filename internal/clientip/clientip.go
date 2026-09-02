// Package clientip derives the address a request should be attributed to,
// for rate limiting and for the session audit trail.
//
// It exists because both call sites previously did this:
//
//	ip := r.RemoteAddr
//	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
//		ip = xff
//	}
//
// X-Forwarded-For is appended to by each hop, so the left-hand part of it
// is whatever the client typed. Taking the whole header as a value made it
// entirely caller-controlled, and the rate limiter used that value as its
// per-visitor map key — so varying the header per request produced a fresh
// limiter every time and the /login limit could not fire. The same value
// was written to sessions.ip_address, making the audit trail caller-writable.
//
// This package lives outside internal/auth because internal/middleware
// already imports internal/auth; putting it there would be an import cycle.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// cloudflareHeader carries the true client address as seen by Cloudflare.
// Cloudflare overwrites any value the caller supplies, so unlike
// X-Forwarded-For it cannot be forged by a client whose traffic actually
// passes through the edge.
//
// This is deliberately NOT X-Forwarded-For. In this deployment the pod's
// RemoteAddr is always the in-cluster Traefik address, so without this
// header every visitor would share a single rate-limit bucket.
//
// Caveat worth knowing before relying on it: a caller who reaches the
// origin directly, bypassing Cloudflare, can set this header themselves.
// Closing that requires restricting the ingress to Cloudflare's published
// ranges, which is an infrastructure change and not done here.
// Spelled in Go's canonical form. net/http canonicalizes header keys on
// both Get and Set, so this matches the "CF-Connecting-IP" sent on the wire.
const cloudflareHeader = "Cf-Connecting-Ip"

// From returns the address to attribute the request to. It prefers the
// Cloudflare-supplied client IP and otherwise falls back to the immediate
// peer. It never returns a caller-controlled value that has not been
// parsed as an IP address.
func From(r *http.Request) string {
	// Values, not Get: Get returns the FIRST value, and Cloudflare's public
	// documentation does not state whether a client-supplied header is
	// overwritten or appended to. If it is ever appended, the edge's value
	// is the last one and a forged first value must not win. Reading the
	// last element is correct under either behavior and costs nothing.
	values := r.Header.Values(cloudflareHeader)
	if len(values) > 0 {
		if v := strings.TrimSpace(values[len(values)-1]); v != "" {
			// Parse rather than pass through: the return value becomes a
			// map key and a database column, so it must be a real address
			// and not an arbitrary caller-supplied string of arbitrary
			// length. The net.IP round-trip also canonicalizes, so two
			// spellings of one address cannot become two rate-limit
			// buckets — do not "simplify" this by returning v directly.
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is normally "host:port", but not every transport sets
		// a port. Fall back to the raw value rather than returning "",
		// which would collapse every caller into one bucket.
		return r.RemoteAddr
	}
	return host
}
