package api

import (
	"net/netip"
	"testing"
	"time"
)

func TestAuthenticationProxyTrustBoundary(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for _, test := range []struct{ name, remote, forwarded, want string }{
		{"untrusted cannot spoof", "203.0.113.1:123", "198.51.100.4", "203.0.113.1"},
		{"trusted edge", "10.0.0.2:80", "198.51.100.4", "198.51.100.4"},
		{"untrusted boundary in chain", "10.0.0.2:80", "192.0.2.9, 198.51.100.4, 10.0.0.3", "198.51.100.4"},
		{"malformed chain", "10.0.0.2:80", "bad-address", "10.0.0.2"},
		{"mapped IPv4", "[::ffff:10.0.0.2]:80", "198.51.100.4", "198.51.100.4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := trustedClientIP(test.remote, test.forwarded, trusted); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
	if got := trustedClientIP("10.0.0.2:80", "198.51.100.4", nil); got != "10.0.0.2" {
		t.Fatal("default trusted proxy set was not empty")
	}
}
func TestAuthenticationLimiterExpiryAndIsolation(t *testing.T) {
	var limiter authenticationLimiter
	now := time.Now()
	if !limiter.allow("a", 2, now) || !limiter.allow("a", 2, now) || limiter.allow("a", 2, now) {
		t.Fatal("attempt budget not enforced")
	}
	if !limiter.allow("b", 2, now) {
		t.Fatal("unrelated client throttled")
	}
	if !limiter.allow("a", 2, now.Add(time.Minute)) {
		t.Fatal("expired budget not released")
	}
}
