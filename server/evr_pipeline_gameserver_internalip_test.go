package server

import (
	"net"
	"testing"
)

// TestIsInternalIP covers the issue #465 private/internal predicate used at
// game-server registration. "Internal" means an address a remote client on the
// public internet cannot reach: RFC1918 (10/8, 172.16/12, 192.168/16), RFC6598
// CGNAT (100.64/10), loopback, and link-local. Anything else (a routable public
// address) is NOT internal and must be normalized out of the internal slot.
func TestIsInternalIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "rfc1918 10/8", ip: "10.1.2.3", want: true},
		{name: "rfc1918 172.16/12", ip: "172.16.5.5", want: true},
		{name: "rfc1918 192.168/16", ip: "192.168.1.10", want: true},
		{name: "cgnat 100.64/10 low", ip: "100.64.0.1", want: true},
		{name: "cgnat 100.64/10 high", ip: "100.127.255.254", want: true},
		{name: "loopback", ip: "127.0.0.1", want: true},
		{name: "link-local", ip: "169.254.1.1", want: true},
		{name: "public", ip: "203.0.113.7", want: false},
		{name: "public just outside cgnat", ip: "100.128.0.1", want: false},
		{name: "public google dns", ip: "8.8.8.8", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := isInternalIP(ip); got != tc.want {
				t.Errorf("isInternalIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestNormalizeInternalIP covers the issue #465 normalize behavior at
// registration: a PUBLIC internal IP is dropped to nil (server still registers
// via its external IP), a private/CGNAT internal IP is kept, and an empty
// (nil) internal stays empty. normalizeInternalIP reports whether it dropped a
// public address so the caller can warn the operator.
func TestNormalizeInternalIP(t *testing.T) {
	tests := []struct {
		name        string
		in          net.IP
		wantKept    bool // expected non-nil result
		wantDropped bool
	}{
		{name: "public dropped", in: net.ParseIP("203.0.113.7"), wantKept: false, wantDropped: true},
		{name: "private kept", in: net.ParseIP("192.168.1.10"), wantKept: true, wantDropped: false},
		{name: "cgnat kept", in: net.ParseIP("100.64.0.1"), wantKept: true, wantDropped: false},
		{name: "loopback kept", in: net.ParseIP("127.0.0.1"), wantKept: true, wantDropped: false},
		{name: "nil stays nil", in: nil, wantKept: false, wantDropped: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, dropped := normalizeInternalIP(tc.in)
			if (out != nil) != tc.wantKept {
				t.Errorf("normalizeInternalIP(%v) kept = %v (out=%v), want kept %v", tc.in, out != nil, out, tc.wantKept)
			}
			if dropped != tc.wantDropped {
				t.Errorf("normalizeInternalIP(%v) dropped = %v, want %v", tc.in, dropped, tc.wantDropped)
			}
			if tc.wantKept && !out.Equal(tc.in) {
				t.Errorf("normalizeInternalIP(%v) = %v, want unchanged", tc.in, out)
			}
		})
	}
}
