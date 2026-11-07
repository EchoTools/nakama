package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// A session that proved its identity out of band (a valid token on the socket, or a Bearer
// token on the login websocket) is not asked to verify a new IP at game login. Every other
// login keeps the new-location check.
//
// The check itself needs a database and a Discord gateway to reach end to end, so these
// tests pin the decision and its log record. The message a non-token login receives is
// pinned by evr_pipeline_login_ipverify_test.go.
func TestNewLocationCheckNotRequired(t *testing.T) {
	xpid := evr.EvrId{PlatformCode: 4, AccountId: 123456789}
	uid := uuid.Must(uuid.NewV4())

	cases := []struct {
		name         string
		userID       uuid.UUID
		wsAuth       bool
		ipAuthorized bool
		wantSkip     bool
		wantLog      bool
	}{
		{
			// The reported case: the socket was authenticated by token (userID set by
			// socket_ws.go), so IsWebsocketAuthenticated is never set. The new IP must not
			// trigger a LoginFailure, and the skip must leave a record.
			name:   "token session from a new IP is not asked to verify",
			userID: uid, wantSkip: true, wantLog: true,
		},
		{
			name:   "websocket-authenticated session from a new IP is not asked to verify",
			wsAuth: true, wantSkip: true, wantLog: true,
		},
		{
			// Unchanged: no authenticated identity, new IP, the check runs and the
			// player gets the code (see the caller and ipVerificationLocationError).
			name:     "unauthenticated login from a new IP is still checked",
			wantSkip: false, wantLog: false,
		},
		{
			// Unchanged: a known IP needs no check, and the skip is not logged because
			// nothing was skipped.
			name:         "token session from a known IP is unchanged",
			userID:       uid,
			ipAuthorized: true,
			wantSkip:     true,
			wantLog:      false,
		},
		{
			name:         "unauthenticated login from a known IP is unchanged",
			ipAuthorized: true,
			wantSkip:     true,
			wantLog:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			// loginRequest tags the logger with the xpid before it reaches the check.
			logger := zap.New(core).With(zap.String("xpid", xpid.String()))
			session := &sessionWS{userID: tc.userID}
			params := &SessionParameters{IsWebsocketAuthenticated: tc.wsAuth}

			got := newLocationCheckNotRequired(logger, session, params, tc.ipAuthorized)
			if got != tc.wantSkip {
				t.Fatalf("newLocationCheckNotRequired() = %t, want %t", got, tc.wantSkip)
			}

			entries := logs.All()
			if !tc.wantLog {
				if len(entries) != 0 {
					t.Fatalf("expected no log record, got %d: %+v", len(entries), entries)
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("expected exactly one log record, got %d", len(entries))
			}
			if entries[0].Level != zap.InfoLevel {
				t.Errorf("level = %s, want info", entries[0].Level)
			}
			fields := entries[0].ContextMap()
			if fields["xpid"] != xpid.String() {
				t.Errorf("xpid field = %v, want %s", fields["xpid"], xpid.String())
			}
			if fields["reason"] != "token_authenticated" {
				t.Errorf("reason field = %v, want token_authenticated", fields["reason"])
			}
		})
	}
}
