package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEventInterface verifies that every concrete event type satisfies the
// Event interface and returns the expected EventType() string.
func TestEventInterface(t *testing.T) {
	ts := time.Date(2026, 5, 25, 18, 35, 8, 0, time.UTC)
	base := baseEvent{Timestamp: ts, RawLine: "{}"}
	ident := identityFields{UID: "u", SID: "s", Username: "n"}

	tests := []struct {
		event    Event
		wantType string
	}{
		{&PartyFormed{baseEvent: base, identityFields: ident}, "PartyFormed"},
		{&PartyJoined{baseEvent: base, identityFields: ident}, "PartyJoined"},
		{&MemberFollowAttempt{baseEvent: base, identityFields: ident}, "MemberFollowAttempt"},
		{&LeaderNotInMatch{baseEvent: base, identityFields: ident}, "LeaderNotInMatch"},
		{&SocialFallback{baseEvent: base, identityFields: ident}, "SocialFallback"},
		{&FollowLeaderLobby{baseEvent: base, identityFields: ident}, "FollowLeaderLobby"},
		{&MatchJoinAttempt{baseEvent: base, identityFields: ident}, "MatchJoinAttempt"},
		{&MatchJoined{baseEvent: base, identityFields: ident}, "MatchJoined"},
		{&MatchJoinFailed{baseEvent: base, identityFields: ident}, "MatchJoinFailed"},
		{&MatchBuilt{baseEvent: base}, "MatchBuilt"},
		{&FollowReleasedIndependent{baseEvent: base, identityFields: ident}, "FollowReleasedIndependent"},
		{&LeaderNonJoinableMode{baseEvent: base, identityFields: ident}, "LeaderNonJoinableMode"},
		{&LeaderNonJoinablePoll{baseEvent: base, identityFields: ident}, "LeaderNonJoinablePoll"},
		{&LeaderLeftDuringPoll{baseEvent: base, identityFields: ident}, "LeaderLeftDuringPoll"},
		{&FailedJoinLeaderLobby{baseEvent: base, identityFields: ident}, "FailedJoinLeaderLobby"},
		{&FailedPriorityJoinLeader{baseEvent: base, identityFields: ident}, "FailedPriorityJoinLeader"},
		{&FailedPriorityJoinFollower{baseEvent: base, identityFields: ident}, "FailedPriorityJoinFollower"},
		{&CrashDetected{baseEvent: base, identityFields: ident}, "CrashDetected"},
		{&PlayerLeft{baseEvent: base, identityFields: ident}, "PlayerLeft"},
		{&PlayerLeftVoluntary{baseEvent: base, identityFields: ident}, "PlayerLeftVoluntary"},
		{&SplitRepaired{baseEvent: base}, "SplitRepaired"},
		{&MatchmakingTicketAdded{baseEvent: base, identityFields: ident}, "MatchmakingTicketAdded"},
		{&EntrantMetadataFallback{baseEvent: base, identityFields: ident}, "EntrantMetadataFallback"},
		{&LobbySessionFailure{baseEvent: base, identityFields: ident}, "LobbySessionFailure"},
		{&LobbyFindComplete{baseEvent: base, identityFields: ident}, "LobbyFindComplete"},
		{&MatchmakingStreamClosed{baseEvent: base, identityFields: ident}, "MatchmakingStreamClosed"},
		{&UnexpectedError{baseEvent: base, identityFields: ident}, "UnexpectedError"},
		{&ReconnectRestore{baseEvent: base, identityFields: ident}, "ReconnectRestore"},
		{&UnknownPartyEvent{baseEvent: base, Msg: "party thing"}, "UnknownPartyEvent"},
	}

	for _, tt := range tests {
		t.Run(tt.wantType, func(t *testing.T) {
			if got := tt.event.EventType(); got != tt.wantType {
				t.Errorf("EventType() = %q, want %q", got, tt.wantType)
			}
			if got := tt.event.EventTimestamp(); !got.Equal(ts) {
				t.Errorf("EventTimestamp() = %v, want %v", got, ts)
			}
			if got := tt.event.EventRawLine(); got != "{}" {
				t.Errorf("EventRawLine() = %q, want %q", got, "{}")
			}
		})
	}
}

// TestBaseEventFields verifies that baseEvent stores and returns fields
// correctly.
func TestBaseEventFields(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	rawLine := `{"msg":"test"}`
	b := baseEvent{Timestamp: ts, RawLine: rawLine}

	if b.EventTimestamp() != ts {
		t.Fatalf("timestamp mismatch: got %v", b.EventTimestamp())
	}
	if b.EventRawLine() != rawLine {
		t.Fatalf("rawline mismatch: got %q", b.EventRawLine())
	}
}

// TestPartyFormedFields verifies field storage on a fully populated event.
func TestPartyFormedFields(t *testing.T) {
	e := &PartyFormed{
		baseEvent:      baseEvent{Timestamp: time.Now(), RawLine: "x"},
		identityFields: identityFields{UID: "uid-1", SID: "sid-1", Username: "alice"},
		LeaderSID:      "leader-sid",
		Size:           3,
		Members:        []string{"bob", "charlie"},
	}

	if e.LeaderSID != "leader-sid" {
		t.Errorf("LeaderSID = %q", e.LeaderSID)
	}
	if e.Size != 3 {
		t.Errorf("Size = %d", e.Size)
	}
	if len(e.Members) != 2 || e.Members[0] != "bob" {
		t.Errorf("Members = %v", e.Members)
	}
	if e.UID != "uid-1" {
		t.Errorf("UID = %q", e.UID)
	}
}

// TestMatchBuiltEntrants verifies that the raw JSON entrants field is
// preserved without parsing.
func TestMatchBuiltEntrants(t *testing.T) {
	entrantsJSON := json.RawMessage(`[{"uid":"a"},{"uid":"b"}]`)
	e := &MatchBuilt{
		baseEvent: baseEvent{Timestamp: time.Now()},
		Entrants:  entrantsJSON,
		Module:    "evr",
	}

	if string(e.Entrants) != `[{"uid":"a"},{"uid":"b"}]` {
		t.Errorf("Entrants mangled: %s", e.Entrants)
	}
	if e.Module != "evr" {
		t.Errorf("Module = %q", e.Module)
	}
}

// TestUnknownPartyEventFields verifies that the catch-all preserves the
// original msg and raw fields.
func TestUnknownPartyEventFields(t *testing.T) {
	fields := map[string]json.RawMessage{
		"msg": json.RawMessage(`"some party message"`),
		"foo": json.RawMessage(`"bar"`),
	}
	e := &UnknownPartyEvent{
		baseEvent: baseEvent{Timestamp: time.Now()},
		Msg:       "some party message",
		Fields:    fields,
	}

	if e.Msg != "some party message" {
		t.Errorf("Msg = %q", e.Msg)
	}
	if string(e.Fields["foo"]) != `"bar"` {
		t.Errorf("Fields[foo] = %s", e.Fields["foo"])
	}
}
