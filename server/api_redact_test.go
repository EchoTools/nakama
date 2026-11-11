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
	if len(entries) == 0 {
		t.Fatal("expected a 'cannot extract client address' warning")
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
