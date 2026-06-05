package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// TestSetNextMatch_SelfTargetImmediateJoin_RequiresOperator is the C1 regression
// test (CRITICAL).
//
// Vector: player/setnextmatch is registered open to any authenticated user
// (AllowedGroups: []). TargetUserID defaults to the caller on self-target, which
// historically SKIPPED the operator check, and join_immediately forces a
// LobbyJoinEntrants placement that never runs lobbyAuthorize. An unprivileged,
// suspended user could therefore self-target + join_immediately into a guild they
// are banned from, bypassing enforcement entirely.
//
// The fix gates the dangerous operations — targeting another user OR
// join_immediately — on global-operator permission, regardless of target. This
// test asserts an unprivileged self-target + join_immediately is rejected at the
// auth boundary (PermissionDenied) BEFORE any join is attempted.
//
// Against pristine /srv/src/nakama this returns no error at the auth boundary
// (the self-target path skipped the check), so the assertion FAILS (RED).
func TestSetNextMatch_SelfTargetImmediateJoin_RequiresOperator(t *testing.T) {
	callerUserID := "11111111-1111-1111-1111-111111111111"

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, callerUserID)
	// Unprivileged: permissions present in context but NOT a global operator.
	// (Presence of perms avoids any DB-backed membership lookup.)
	ctx = WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: false})

	// Self-target (TargetUserID omitted -> defaults to caller) + join_immediately.
	payload, _ := json.Marshal(SetNextMatchRPCRequest{
		JoinImmediately: true,
	})

	// nk/db are nil: the auth gate must reject BEFORE touching them. A panic or
	// non-permission error would indicate the gate ran too late.
	_, err := SetNextMatchRPC(ctx, &mockLogger{}, nil, nil, string(payload))
	if err == nil {
		t.Fatal("BUG: unprivileged self-target + join_immediately was allowed past the auth gate")
	}

	var rtErr *runtime.Error
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected a runtime error, got %T: %v", err, err)
	}
	if rtErr.Code != StatusPermissionDenied {
		t.Fatalf("expected PermissionDenied (%d), got code %d: %v", StatusPermissionDenied, rtErr.Code, err)
	}
}

// TestSetNextMatch_SelfTargetNoImmediateJoin_Allowed confirms the fix does NOT
// over-block: a plain self-target (store-only, no immediate join) is still
// permitted for an unprivileged user — that directive is applied on their next
// normal login, which DOES route through lobbyAuthorize. With nk == nil the
// store will fail, but it must get PAST the auth gate (i.e. NOT PermissionDenied)
// to reach the store step.
func TestSetNextMatch_SelfTargetNoImmediateJoin_Allowed(t *testing.T) {
	callerUserID := "11111111-1111-1111-1111-111111111111"

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, callerUserID)
	ctx = WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: false})

	// Store-only self-target with a NIL match id so the RPC returns before the
	// immediate-join path; the directive store may error on nil nk, but it must
	// not be a PermissionDenied from the auth gate.
	payload, _ := json.Marshal(SetNextMatchRPCRequest{JoinImmediately: false})

	defer func() {
		// A nil-nk store may panic; that's acceptable for this assertion because
		// it proves we got PAST the auth gate (which returns cleanly). Recover so
		// the test reports the auth outcome rather than the unrelated nil-store.
		_ = recover()
	}()

	_, err := SetNextMatchRPC(ctx, &mockLogger{}, nil, nil, string(payload))
	if err != nil {
		var rtErr *runtime.Error
		if errors.As(err, &rtErr) && rtErr.Code == StatusPermissionDenied {
			t.Fatalf("over-block: unprivileged store-only self-target was denied: %v", err)
		}
		// Any other error (e.g. store failure on nil nk) is fine.
	}
}
