package server

// SEC-3 regression tests: userServerProfileUpdateRequest must only apply
// server-profile stat updates when the sender is the authoritative game server
// (the broadcaster session bound to the referenced match). See BUGS.md SEC-3.
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
	pipeline  *EvrPipeline
	nk        *sec3MockNK
	label     *MatchLabel
	payload   *evr.UpdatePayload
	evrID     evr.EvrId
	serverSID uuid.UUID // the match's authoritative game server session
}

func newSEC3Fixture(t *testing.T) *sec3Fixture {
	t.Helper()

	serverSID := uuid.Must(uuid.NewV4())
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
		ID:         MatchID{UUID: matchUUID, Node: "node"},
		Mode:       evr.ModeCombatPublic,
		GroupID:    &groupID,
		GameServer: &GameServerPresence{SessionID: serverSID},
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
		pipeline:  &EvrPipeline{},
		nk:        &sec3MockNK{profileJSON: string(profileJSON)},
		label:     label,
		payload:   payload,
		evrID:     evrID,
		serverSID: serverSID,
	}
}

// TestSEC3_ProfileUpdate_RejectsNonAuthoritativeSender proves that a sender that
// is NOT the match's authoritative game server cannot get a stat update applied.
//
// RED (pre-fix): the update is applied (nk.Event called) even though the sender
// is an ordinary session, not the broadcaster -> len(events) == 1 -> FAIL.
// GREEN (post-fix): the update is rejected with a warn log -> len(events) == 0.
func TestSEC3_ProfileUpdate_RejectsNonAuthoritativeSender(t *testing.T) {
	f := newSEC3Fixture(t)

	// An ordinary authenticated session that is NOT the match's game server.
	attackerSID := uuid.Must(uuid.NewV4())
	if attackerSID == f.serverSID {
		t.Fatal("attacker session collided with server session")
	}

	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	err := f.pipeline.processUserServerProfileUpdate(
		context.Background(), logger, f.nk, attackerSID, f.evrID, f.label, f.payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.nk.events) != 0 {
		t.Fatalf("SEC-3: non-authoritative sender %s had a stat update APPLIED (%d event(s)); expected rejection with zero events",
			attackerSID, len(f.nk.events))
	}

	if logs.FilterMessageSnippet("non-authoritative").Len() == 0 {
		t.Errorf("expected a warn-level log for the rejected non-authoritative sender; got: %v", logs.All())
	}
}

// TestSEC3_ProfileUpdate_AcceptsAuthoritativeBroadcaster proves the fix does not
// over-reject: the legitimate game-server session (the broadcaster bound to the
// match) still has its stat update applied.
func TestSEC3_ProfileUpdate_AcceptsAuthoritativeBroadcaster(t *testing.T) {
	f := newSEC3Fixture(t)

	logger := zap.NewNop()

	// Sender IS the match's authoritative game server.
	err := f.pipeline.processUserServerProfileUpdate(
		context.Background(), logger, f.nk, f.serverSID, f.evrID, f.label, f.payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.nk.events) != 1 {
		t.Fatalf("legitimate broadcaster update was not applied: got %d event(s), want 1", len(f.nk.events))
	}
	if got := f.nk.events[0].Name; got != "*server.EventServerProfileUpdate" {
		t.Errorf("unexpected event name: %q", got)
	}
}
