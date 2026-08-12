package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// --- CF-Connecting-IP spoofing regression tests -----------------------------
//
// session_ws.go accepted CF-Connecting-IP from any peer and copied it straight
// into session.clientIP with no trusted-proxy check and no net.ParseIP. That
// address is the input to VPN classification, alt detection, the IP denial
// index and every audit log, so a client could name whatever address it liked
// for all of them. The connect log fired BEFORE the override, so the spoofed
// value never appeared in "New WebSocket session connected" -- which is why
// this survived an entire outage investigation unseen.

// mustPrefixes is the parsed form of a socket.trusted_proxies list.
func mustPrefixes(t *testing.T, entries ...string) []netip.Prefix {
	t.Helper()
	prefixes, err := parseTrustedProxies(entries)
	if err != nil {
		t.Fatalf("parseTrustedProxies(%v): %v", entries, err)
	}
	return prefixes
}

// requestFrom builds a request as it arrives at NewSessionWS: RemoteAddr is
// the TCP peer (the only address a client cannot choose) and headers are
// entirely attacker-controlled.
func requestFrom(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ws?format=evr", nil)
	r.RemoteAddr = peer
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// The core defect: no trusted proxy is configured, so no forwarding header may
// be honoured, yet a direct client's CF-Connecting-IP became its client IP.
func TestCFConnectingIP_IgnoredFromUntrustedPeer(t *testing.T) {
	req := requestFrom("203.0.113.7:51234", map[string]string{
		"CF-Connecting-IP": "1.2.3.4",
	})

	got := newSessionClientIP(zap.NewNop(), req, SessionFormatEVR, "203.0.113.7", "51234", nil)

	if got != "203.0.113.7" {
		t.Fatalf("spoofed CF-Connecting-IP was honoured from an untrusted peer: client_ip=%q, want the peer address %q", got, "203.0.113.7")
	}
}

// Same, with a trusted-proxy list configured that the peer is not in.
func TestCFConnectingIP_IgnoredFromPeerOutsideTrustedRange(t *testing.T) {
	trusted := mustPrefixes(t, "173.245.48.0/20", "2400:cb00::/32")
	req := requestFrom("203.0.113.7:51234", map[string]string{
		"CF-Connecting-IP": "1.2.3.4",
	})

	got := newSessionClientIP(zap.NewNop(), req, SessionFormatEVR, "203.0.113.7", "51234", trusted)

	if got != "203.0.113.7" {
		t.Fatalf("spoofed CF-Connecting-IP was honoured from a peer outside the trusted ranges: client_ip=%q, want %q", got, "203.0.113.7")
	}
}

// The legitimate case must keep working: a peer inside a configured range is a
// real reverse proxy, and its forwarded address is the true client.
func TestCFConnectingIP_HonouredFromTrustedProxy(t *testing.T) {
	trusted := mustPrefixes(t, "173.245.48.0/20")
	req := requestFrom("173.245.48.9:443", map[string]string{
		"CF-Connecting-IP": "1.2.3.4",
	})

	got := newSessionClientIP(zap.NewNop(), req, SessionFormatEVR, "173.245.48.9", "443", trusted)

	if got != "1.2.3.4" {
		t.Fatalf("CF-Connecting-IP from a trusted proxy was not honoured: client_ip=%q, want %q", got, "1.2.3.4")
	}
}

// A single-host entry ("192.0.2.1") is as valid a trusted_proxies entry as a
// CIDR range, and an IPv6 proxy must work too.
func TestCFConnectingIP_TrustedProxyForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		peer    string
		want    string
	}{
		{"bare IPv4 host", []string{"192.0.2.10"}, "192.0.2.10:443", "1.2.3.4"},
		{"bare IPv4 host, different peer", []string{"192.0.2.10"}, "192.0.2.11:443", "192.0.2.11"},
		{"IPv6 range", []string{"2400:cb00::/32"}, "[2400:cb00:1::5]:443", "1.2.3.4"},
		{"IPv6 outside range", []string{"2400:cb00::/32"}, "[2001:db8::5]:443", "2001:db8::5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trusted := mustPrefixes(t, tc.entries...)
			req := requestFrom(tc.peer, map[string]string{"CF-Connecting-IP": "1.2.3.4"})
			peerIP, peerPort := extractClientAddress(zap.NewNop(), tc.peer, req, "test")
			got := newSessionClientIP(zap.NewNop(), req, SessionFormatEVR, peerIP, peerPort, trusted)
			if got != tc.want {
				t.Fatalf("client_ip=%q, want %q", got, tc.want)
			}
		})
	}
}

// Even from a trusted proxy the header value is data, not an address, until it
// parses. Without net.ParseIP the string went on to be interpolated into a
// Bluge regex query (see LoginDeniedClientIPAddressSearch).
func TestCFConnectingIP_MalformedValueIgnored(t *testing.T) {
	trusted := mustPrefixes(t, "173.245.48.0/20")
	for _, value := range []string{
		"not-an-ip",
		"[^x]*",
		"1.2.3.4, 5.6.7.8",
		"1.2.3.4/24",
		"999.999.999.999",
	} {
		t.Run(value, func(t *testing.T) {
			req := requestFrom("173.245.48.9:443", map[string]string{"CF-Connecting-IP": value})
			got := newSessionClientIP(zap.NewNop(), req, SessionFormatEVR, "173.245.48.9", "443", trusted)
			if got != "173.245.48.9" {
				t.Fatalf("malformed CF-Connecting-IP %q was accepted as client_ip=%q, want the proxy peer %q", value, got, "173.245.48.9")
			}
		})
	}
}

// The connect log must carry the address the session actually runs as. It
// logged the pre-override value, so the one line an operator greps for during
// an incident showed the address the attacker was NOT using.
func TestConnectLogCarriesResolvedClientIP(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	trusted := mustPrefixes(t, "173.245.48.0/20")
	req := requestFrom("173.245.48.9:443", map[string]string{"CF-Connecting-IP": "1.2.3.4"})

	got := newSessionClientIP(zap.New(core), req, SessionFormatEVR, "173.245.48.9", "443", trusted)

	entries := logs.FilterMessage("New WebSocket session connected").All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one connect log entry, got %d", len(entries))
	}
	logged, ok := entries[0].ContextMap()["client_ip"].(string)
	if !ok {
		t.Fatalf("connect log has no client_ip field: %v", entries[0].ContextMap())
	}
	if logged != got {
		t.Fatalf("connect log fired before IP resolution: logged client_ip=%q but session runs as %q", logged, got)
	}
	if logged != "1.2.3.4" {
		t.Fatalf("connect log client_ip=%q, want the resolved address %q", logged, "1.2.3.4")
	}
}

// A rejected header must still be visible somewhere: silently dropping it
// leaves an operator with no signal that anyone is probing.
func TestCFConnectingIP_RejectionIsLogged(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	req := requestFrom("203.0.113.7:51234", map[string]string{"CF-Connecting-IP": "1.2.3.4"})

	_ = newSessionClientIP(zap.New(core), req, SessionFormatEVR, "203.0.113.7", "51234", nil)

	if logs.Len() == 0 {
		t.Fatal("an ignored CF-Connecting-IP header produced no warning; a probe would be invisible")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty list trusts nothing", func(t *testing.T) {
		prefixes, err := parseTrustedProxies(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(prefixes) != 0 {
			t.Fatalf("expected no prefixes, got %v", prefixes)
		}
		if isTrustedProxy("203.0.113.7:443", prefixes) {
			t.Fatal("empty trusted_proxies must trust no peer")
		}
	})

	t.Run("invalid entry is an error, not a silent drop", func(t *testing.T) {
		for _, entry := range []string{"not-an-ip", "192.0.2.0/33", "192.0.2.1/", "1.2.3"} {
			if _, err := parseTrustedProxies([]string{entry}); err == nil {
				t.Errorf("parseTrustedProxies([%q]) returned no error", entry)
			}
		}
	})

	t.Run("peer without a port is handled", func(t *testing.T) {
		trusted := mustPrefixes(t, "192.0.2.0/24")
		if !isTrustedProxy("192.0.2.5", trusted) {
			t.Fatal("bare peer address in a trusted range was not recognised")
		}
	})
}
