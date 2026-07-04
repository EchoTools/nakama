package server

// SEC-3 regression tests: userServerProfileUpdateRequest must only apply
// server-profile stat updates when the sender is the authoritative game server,
// identified by the operator (user) that registered the server bound to the
// referenced match. See BUGS.md SEC-3.
//
// Pre-nevr-runtime, the server-profile update arrives over a DIFFERENT
// connection than the game server's own registered session, so the sender's
// session ID does not match label.GameServer.SessionID. Authority is therefore
// checked against label.GameServer.OperatorID (the user id of the server) vs
// the sender session's authenticated user id.
//
// Before the fix, processUserServerProfileUpdate applied the update for any
// caller as long as the target EvrID was a player in the match and a group
// member — with no check that the *sender* was the match's game server. That
// let any authenticated client inject stats for a live non-arena match whose
// session ID it knew (integrity / IDOR).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// sec3MockNK is a minimal NakamaModule stub for processUserServerProfileUpdate.
// It records every Event() call so a test can assert whether a stat update was
// actually applied (SendEvent -> nk.Event).
type sec3MockNK struct {
	runtime.NakamaModule
	profileJSON string
	events      []*api.Event
}

func (n *sec3MockNK) AccountGetId(_ context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}}, nil
}

func (n *sec3MockNK) StorageRead(_ context.Context, _ []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return []*api.StorageObject{{Value: n.profileJSON, Version: "1"}}, nil
}

func (n *sec3MockNK) Event(_ context.Context, evt *api.Event) error {
	n.events = append(n.events, evt)
	return nil
}

// sec3Fixture builds a live non-arena (combat) match label plus the mock nk and
// payload that a legitimate server-profile update would carry.
type sec3Fixture struct {
	pipeline    *EvrPipeline
	nk          *sec3MockNK
	label       *MatchLabel
	payload     *evr.UpdatePayload
	evrID       evr.EvrId
	serverOpsID uuid.UUID // the operator (user id) of the match's authoritative game server
}

func newSEC3Fixture(t *testing.T) *sec3Fixture {
	t.Helper()

	serverOpsID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	matchUUID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4()).String()
	evrID := evr.EvrId{PlatformCode: 4, AccountId: 1234567890}

	// The target player is a member of the group in a combat match.
	profile := &EVRProfile{
		InGameNames: map[string]GroupInGameName{
			groupID.String(): {GroupID: groupID.String(), DisplayName: "Tester"},
		},
	}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("failed to marshal profile: %v", err)
	}

	label := &MatchLabel{
		ID:      MatchID{UUID: matchUUID, Node: "node"},
		Mode:    evr.ModeCombatPublic,
		GroupID: &groupID,
		// SessionID is deliberately distinct from the operator id: authority is
		// checked against OperatorID, not SessionID (the update arrives over a
		// different connection than the game server's registered session).
		GameServer: &GameServerPresence{
			SessionID:  uuid.Must(uuid.NewV4()),
			OperatorID: serverOpsID,
		},
		Players: []PlayerInfo{
			{
				EvrID:       evrID,
				Team:        BlueTeam,
				UserID:      userID,
				DisplayName: "Tester",
				SessionID:   uuid.Must(uuid.NewV4()).String(),
			},
		},
	}

	payload := &evr.UpdatePayload{
		MatchType: int64(evr.ModeCombatPublic),
		Update: evr.ServerProfileUpdate{
			Statistics: &evr.ServerProfileUpdateStatistics{
				Combat: &evr.CombatStatistics{},
			},
		},
	}

	return &sec3Fixture{
		pipeline:    &EvrPipeline{},
		nk:          &sec3MockNK{profileJSON: string(profileJSON)},
		label:       label,
		payload:     payload,
		evrID:       evrID,
		serverOpsID: serverOpsID,
	}
}

// TestSEC3_ProfileUpdate_RejectsNonAuthoritativeOperator proves that a sender
// whose operator (user id) is NOT the match's authoritative game server operator
// cannot get a stat update applied.
//
// RED (pre-fix): the update is applied (nk.Event called) even though the sender
// is not the operator -> len(events) == 1 -> FAIL.
// GREEN (post-fix): the update is rejected with a warn log -> len(events) == 0.
func TestSEC3_ProfileUpdate_RejectsNonAuthoritativeOperator(t *testing.T) {
	f := newSEC3Fixture(t)

	// An ordinary authenticated user that is NOT the match's server operator.
	attackerOperatorID := uuid.Must(uuid.NewV4())
	if attackerOperatorID == f.serverOpsID {
		t.Fatal("attacker operator collided with server operator")
	}

	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	err := f.pipeline.processUserServerProfileUpdate(
		context.Background(), logger, f.nk, attackerOperatorID, f.evrID, f.label, f.payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.nk.events) != 0 {
		t.Fatalf("SEC-3: non-authoritative operator %s had a stat update APPLIED (%d event(s)); expected rejection with zero events",
			attackerOperatorID, len(f.nk.events))
	}

	if logs.FilterMessageSnippet("non-authoritative").Len() == 0 {
		t.Errorf("expected a warn-level log for the rejected non-authoritative operator; got: %v", logs.All())
	}
}

// TestSEC3_ProfileUpdate_AcceptsAuthoritativeOperator proves the fix does not
// over-reject: a sender whose authenticated user id matches the match's game
// server operator still has its stat update applied — even though the sender's
// session is a different connection than the game server's registered session.
func TestSEC3_ProfileUpdate_AcceptsAuthoritativeOperator(t *testing.T) {
	f := newSEC3Fixture(t)

	logger := zap.NewNop()

	// Sender's operator IS the match's authoritative game server operator.
	err := f.pipeline.processUserServerProfileUpdate(
		context.Background(), logger, f.nk, f.serverOpsID, f.evrID, f.label, f.payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.nk.events) != 1 {
		t.Fatalf("legitimate operator update was not applied: got %d event(s), want 1", len(f.nk.events))
	}
	if got := f.nk.events[0].Name; got != "*server.EventServerProfileUpdate" {
		t.Errorf("unexpected event name: %q", got)
	}
}
