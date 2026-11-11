package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/metadata"
)

// When the client address cannot be parsed, the warning used to dump every
// request header (and, for gRPC, the whole context) -- including Authorization
// and Cookie. No credential may reach the log.
func TestExtractClientAddressDoesNotLogCredentials(t *testing.T) {
	const secret = "SECRET-CREDENTIAL-VALUE"

	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)

	r, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	r.RemoteAddr = "garbage"
	r.Header.Set("X-Forwarded-For", "a[b:1")
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Cookie", "refreshToken="+secret)
	r.Header.Set("X-Api-Key", secret)
	r.Header.Set("User-Agent", "visible-agent")
	extractClientAddressFromRequest(logger, r)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+secret, "x-forwarded-for", "a[b:1"))
	extractClientAddressFromContext(logger, ctx)

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("expected two 'cannot extract client address' warnings (request, context), got %d", len(entries))
	}
	// The gRPC warning must carry the incoming metadata, with the credential redacted.
	if got := fmt.Sprintf("%v", entries[1].ContextMap()); !strings.Contains(got, "authorization:[[REDACTED]]") {
		t.Errorf("gRPC warning should log metadata with authorization redacted, got: %s", got)
	}
	for _, e := range entries {
		line := fmt.Sprintf("%v %v", e.Message, e.ContextMap())
		if strings.Contains(line, secret) {
			t.Errorf("credential leaked into log: %s", line)
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", entries[0].ContextMap()), "visible-agent") {
		t.Errorf("non-sensitive headers should still be logged: %v", entries[0].ContextMap())
	}
}

// Only credential values are masked. Sec-WebSocket-Key is a public handshake
// nonce and must keep its real value in the log (socket_ws.go, nevr_ws_acceptor.go).
func TestRedactSensitiveHeadersMasksOnlyCredentials(t *testing.T) {
	h := http.Header{}
	for k, v := range map[string]string{
		"Authorization": "a", "Proxy-Authorization": "a", "Cookie": "a", "Set-Cookie": "a",
		"X-Api-Key": "a", "X-Refresh-Token": "a", "Client-Secret": "a", "X-Db-Password": "a",
		"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "X-Authenticated-User": "bob",
		"Www-Authenticate": "Bearer", "User-Agent": "ua",
	} {
		h.Set(k, v)
	}
	got := redactSensitiveHeaders(h)
	for _, k := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Refresh-Token", "Client-Secret", "X-Db-Password"} {
		if v := got[http.CanonicalHeaderKey(k)]; len(v) != 1 || v[0] != "[REDACTED]" {
			t.Errorf("%s = %v; want [REDACTED]", k, v)
		}
	}
	if v := got["Sec-Websocket-Key"]; len(v) != 1 || v[0] != "dGhlIHNhbXBsZSBub25jZQ==" {
		t.Errorf("Sec-WebSocket-Key = %v; want its real value", v)
	}
	for _, k := range []string{"X-Authenticated-User", "Www-Authenticate", "User-Agent"} {
		if v := got[k]; len(v) != 1 || v[0] == "[REDACTED]" {
			t.Errorf("%s = %v; want unredacted", k, v)
		}
	}
}
